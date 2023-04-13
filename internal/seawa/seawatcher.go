package seawa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/benleb/gloomberg/internal"
	"github.com/benleb/gloomberg/internal/nemo/osmodels"
	"github.com/benleb/gloomberg/internal/style"
	"github.com/charmbracelet/log"
	"github.com/go-redis/redis/v8"
	"github.com/nshafer/phx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/viper"
)

type MgmtAction int64

const (
	// client actions.
	Subscribe MgmtAction = iota
	Unsubscribe

	// server actions.

	// SendSlugs is used to request the slugs/events to subscribe to from the clients.
	SendSlugs
)

func (a MgmtAction) String() string {
	switch a {
	case Subscribe:
		return "Subscribe"
	case Unsubscribe:
		return "Unsubscribe"
	case SendSlugs:
		return "SendSlugs"
	default:
		return "unknown"
	}
}

type MgmtEvent struct {
	Action MgmtAction           `json:"action"`
	Events []osmodels.EventType `json:"events"`
	Slugs  []string             `json:"slugs"`
}

type SeaWatcher struct {
	// channel for events received from the opensea stream
	receivedEvents chan map[string]interface{}

	// phoenix channels client
	phoenixSocket *phx.Socket

	// subscribed phoenix channels
	channels map[string]*phx.Channel

	// subscribed slugs/events
	subscriptions map[osmodels.EventType]map[string]func()

	// redis client
	rdb *redis.Client

	mu *sync.Mutex
}

var (
	AvailableEventTypes = []osmodels.EventType{osmodels.ItemListed, osmodels.ItemSold, osmodels.ItemReceivedOffer} // ItemMetadataUpdated, ItemCancelled

	eventsReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gloomberg_oswatcher_events_received_total",
		Help: "The total number of received events from the opensea api/stream",
	})
	// eventsProcessedTotal = promauto.NewCounter(prometheus.CounterOpts{
	// 	Name: "gloomberg_oswatcher_events_processed_total",
	// 	Help: "The total number of processed events from the opensea api/stream",
	// })
	// eventsIgnoredTotal = promauto.NewCounter(prometheus.CounterOpts{
	// 	Name: "gloomberg_oswatcher_events_ignored_total",
	// 	Help: "The total number of ignored events from the opensea api/stream",
	// }).
)

var seaWatcher *SeaWatcher

func NewStreamWatcher(apiToken string, rdb *redis.Client) *SeaWatcher {
	if seaWatcher != nil {
		return seaWatcher
	}

	var socket *phx.Socket

	if apiToken != "" {
		endpointURL := fmt.Sprint(osmodels.StreamAPIEndpoint, "?token=", apiToken)

		endpoint, err := url.Parse(endpointURL)
		if err != nil {
			log.Info(err)

			return nil
		}

		// create phoenix socket
		socket = phx.NewSocket(endpoint)
		socket.Logger = phx.NewSimpleLogger(phx.LoggerLevel(phx.LogError))

		socket.ReconnectAfterFunc = func(attempt int) time.Duration {
			log.Warn(fmt.Sprintf("⚓️❕ opensea stream socket retry after %v..", time.Duration(attempt)*2*time.Second))

			return time.Duration(attempt) * 2 * time.Second
		}

		// error function
		onError := func(err error) { log.Info(err) }
		socket.OnError(onError)

		socket.OnClose(func() {
			log.Info("⚓️❕ opensea stream socket closed, reconnecting...")

			err := socket.Reconnect()
			if err != nil {
				onError(errors.New("opensea stream socket reconnecting failed: " + err.Error()))
			}
		})
	}

	client := &SeaWatcher{
		receivedEvents: make(chan map[string]interface{}, 1024),
		subscriptions:  make(map[osmodels.EventType]map[string]func(), 0),

		phoenixSocket: socket,
		channels:      make(map[string]*phx.Channel),

		rdb: rdb,

		mu: &sync.Mutex{},
	}

	// create subscriptions map/registry
	for _, event := range AvailableEventTypes {
		client.subscriptions[event] = make(map[string]func(), 0)
	}

	if client.phoenixSocket != nil {
		if err := client.connect(); err != nil {
			socketError := errors.New("opensea stream socket connection failed: " + err.Error())
			log.Error("⚓️❌ " + socketError.Error())

			return nil
		}
	}

	seaWatcher = client

	// subscribe to management channel
	seaWatcher.subscribeToMgmtChannel()

	// publish a "SendSlugs" event to the management channel to request the slugs/events to subscribe to from the clients
	seaWatcher.publishSendSlugs()

	return seaWatcher
}

func (sw *SeaWatcher) EventChannel() chan map[string]interface{} {
	return sw.receivedEvents
}

func (sw *SeaWatcher) ActiveSubscriptions() map[osmodels.EventType]map[string]func() {
	return sw.subscriptions
}

func (sw *SeaWatcher) connect() error {
	return sw.phoenixSocket.Connect()
}

// func (sw *SeaWatcher) disconnect() error {
// 	log.Info("Successfully disconnected from socket")
// 	sw.channels = make(map[string]*phx.Channel)
// 	return sw.phoenixSocket.Disconnect()
// }

// eventHandler handles incoming stream api events and forwards them as map.
func (sw *SeaWatcher) eventHandler(response any) {
	eventsReceivedTotal.Inc()

	itemEvent, ok := response.(map[string]interface{})
	if !ok {
		log.Error(fmt.Sprintf("⚓️❌ error in type assertion of received event: %+v", response))

		return
	}

	log.Debug(fmt.Sprintf("⚓️ received event: %+v", itemEvent))

	sw.receivedEvents <- itemEvent
}

func (sw *SeaWatcher) SubscribeForSlug(eventType osmodels.EventType, slug string) bool {
	sw.mu.Lock()
	alreadySubscribed := sw.subscriptions[eventType][slug]

	if alreadySubscribed != nil {
		sw.mu.Unlock()

		log.Debug(fmt.Sprintf("☕️ already subscribed to %s for %s", eventType, slug))

		return false
	}

	sw.subscriptions[eventType][slug] = sw.on(eventType, slug, sw.eventHandler)
	sw.mu.Unlock()

	return true
}

func (sw *SeaWatcher) UnubscribeForSlug(eventType osmodels.EventType, slug string) bool {
	sw.mu.Lock()
	unsubscribe := sw.subscriptions[eventType][slug]
	sw.mu.Unlock()

	if unsubscribe != nil {
		// unsubscribe
		unsubscribe()

		// remove slug
		sw.mu.Lock()
		sw.subscriptions[eventType][slug] = nil
		sw.mu.Unlock()

		return true
	}

	log.Debug(fmt.Sprintf("☕️ not subscribed to %s for %s (anymore)", eventType, slug))

	return false
}

func (sw *SeaWatcher) createChannel(topic string) *phx.Channel {
	channel := sw.phoenixSocket.Channel(topic, nil)

	join, err := channel.Join()
	if err != nil {
		log.Info(err)

		return nil
	}

	join.Receive("ok", func(_ any) {
		log.Debug(fmt.Sprintf("👋 joined channel: %s", channel.Topic()))
	})

	join.Receive("error", func(response any) {
		log.Warn("failed to joined channel:", channel.Topic(), response)
	})

	sw.channels[topic] = channel

	return channel
}

func (sw *SeaWatcher) getChannel(topic string) *phx.Channel {
	channel, ok := sw.channels[topic]
	if !ok {
		channel = sw.createChannel(topic)
	}

	return channel
}

func (sw *SeaWatcher) on(eventType osmodels.EventType, collectionSlug string, eventHandler func(response any)) func() {
	topic := fmt.Sprintf("collection:%s", collectionSlug)

	log.Debug(fmt.Sprintf("Fetching channel %s", topic))
	channel := sw.getChannel(topic)

	log.Debug(fmt.Sprintf("Subscribing to %s events on %s", eventType, topic))
	channel.On(string(eventType), eventHandler)

	log.Debug(fmt.Sprintf("␚ 🔔 subscribed to %s for %s", string(eventType), collectionSlug))

	return func() {
		log.Info(fmt.Sprintf("Unsubscribing from %s events on %s", eventType, topic))

		leave, err := channel.Leave()
		if err != nil {
			log.Info("channel.Leave err:", err)
		}

		leave.Receive("ok", func(_ any) {
			delete(sw.channels, collectionSlug)
			log.Info(fmt.Sprintf("Successfully left channel %s listening for %s", topic, eventType))
		})
	}
}

// func subscribeToMgmt(sw *seawa.seawa, rdb *redis.Client) {.
func (sw *SeaWatcher) subscribeToMgmtChannel() {
	// subscribe to new slugs
	pubsubMgmt := sw.rdb.Subscribe(context.Background(), internal.TopicSeaWatcherMgmt)
	ch := pubsubMgmt.Channel(redis.WithChannelSize(1024))

	log.Info(fmt.Sprintf("⚓️ subscribed to mgmt channel  %s", pubsubMgmt.String()))

	// loop over incoming events
	go func() {
		for msg := range ch {
			log.Debug(fmt.Sprintf("⚓️ received msg on channel %s: %s", msg.Channel, msg.Payload))

			var mgmtEvent *MgmtEvent

			if err := json.Unmarshal([]byte(msg.Payload), &mgmtEvent); err != nil {
				log.Error(fmt.Sprintf("⚓️❌ error json.Unmarshal: %+v", err))

				continue
			}

			switch mgmtEvent.Action {
			case SendSlugs:
				// SendSlugs can be ignored on server side for now
				continue

			case Subscribe, Unsubscribe:
				log.Info(fmt.Sprintf("⚓️ ␚ received %s for %s collections/slugs on %s, subscribing...", style.BoldStyle.Render(mgmtEvent.Action.String()), style.BoldStyle.Render(fmt.Sprint(len(mgmtEvent.Slugs))), internal.TopicSeaWatcherMgmt))

				if len(mgmtEvent.Slugs) == 0 {
					log.Error("⚓️❌ incoming collection slugs msg is empty")

					continue
				}

				if viper.GetString("api_keys.opensea") == "" {
					log.Error("⚓️❌ opensea api key is not set, can't subscribe to listings")

					continue
				}

				var action func(event osmodels.EventType, slug string) bool

				switch mgmtEvent.Action {
				case Subscribe:
					action = sw.SubscribeForSlug
				case Unsubscribe:
					action = sw.UnubscribeForSlug
				}

				// subscribe to which events?
				if len(mgmtEvent.Events) == 0 {
					// subscribe to all available events if none are specified
					mgmtEvent.Events = AvailableEventTypes
				}

				newSubscriptions := make(map[string][]osmodels.EventType, 0)
				newEventSubscriptions := 0

				for _, slug := range mgmtEvent.Slugs {
					if slug == "ens" {
						log.Info("⚓️ ␚ skipping ens for now")

						continue
					}

					for _, event := range mgmtEvent.Events {
						if action(event, slug) {
							newEventSubscriptions++

							if _, ok := newSubscriptions[slug]; !ok {
								newSubscriptions[slug] = make([]osmodels.EventType, 0)
							}

							newSubscriptions[slug] = append(newSubscriptions[slug], event)

							time.Sleep(137 * time.Millisecond)
						}
					}
				}

				log.Info(fmt.Sprintf(
					"⚓️ ␚ successfully subscribed to %s new collections/slugs (%d events in total) | total subscriptions: %s",
					style.BoldStyle.Render(fmt.Sprint(len(newSubscriptions))),
					newEventSubscriptions,
					style.BoldStyle.Render(fmt.Sprint(len(sw.ActiveSubscriptions()[osmodels.ItemListed]))),
				))

			default:
				log.Info(fmt.Sprintf("⚓️ 👀 received unknown mgmt event: %s", mgmtEvent.Action.String()))

				continue
			}
		}
	}()
}

func (sw *SeaWatcher) publishSendSlugs() {
	// build "SendSlugs" event
	sendSlugsEvent := &MgmtEvent{
		Action: SendSlugs,
	}

	// marshal event
	jsonMgmtEvent, err := json.Marshal(sendSlugsEvent)
	if err != nil {
		log.Error("⚓️❌ marshal failed for SendSlugs action: %s | %v", err, sendSlugsEvent)

		return
	}

	if err := sw.rdb.Publish(context.Background(), internal.TopicSeaWatcherMgmt, jsonMgmtEvent).Err(); err != nil {
		log.Error(fmt.Sprintf("⚓️❌ error publishing %s to redis: %s", sendSlugsEvent.Action.String(), err.Error()))
	} else {
		log.Info(fmt.Sprintf("⚓️ 📢 sent %s event to %s", sendSlugsEvent.Action.String(), internal.TopicSeaWatcherMgmt))
	}
}
