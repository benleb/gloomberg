package gloomberg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/benleb/gloomberg/internal"
	"github.com/benleb/gloomberg/internal/collections"
	"github.com/benleb/gloomberg/internal/degendb"
	"github.com/benleb/gloomberg/internal/gbl"
	"github.com/benleb/gloomberg/internal/nemo/provider"
	"github.com/benleb/gloomberg/internal/nemo/wallet"
	"github.com/benleb/gloomberg/internal/nemo/watch"
	"github.com/benleb/gloomberg/internal/rueidica"
	"github.com/benleb/gloomberg/internal/seawa/models"
	"github.com/benleb/gloomberg/internal/stats"
	"github.com/benleb/gloomberg/internal/style"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/redis/rueidis"
	"github.com/spf13/viper"
)

type Gloomberg struct {
	// Nodes        *nodes.Nodes
	ProviderPool *provider.Pool
	Watcher      *watch.Watcher

	CollectionDB *collections.CollectionDB
	OwnWallets   *wallet.Wallets
	Stats        *stats.Stats

	Rdb    rueidis.Client
	Rueidi *rueidica.Rueidica

	QueueSlugs chan common.Address

	*eventHub
	*degendb.DegenDB
}

type printConfig struct {
	Icon    string
	Keyword string
	Color   lipgloss.Color
}

var prConigs = map[string]printConfig{
	"web": {
		Icon:    "🖥️",
		Keyword: "web",
		Color:   lipgloss.Color("#662288"),
	},
	"wawa": {
		Icon:    "💳",
		Keyword: "wawa",
		Color:   lipgloss.Color("#550933"),
	},
}

func New() *Gloomberg {
	// redis
	rdb := getRedisClient()

	gb := &Gloomberg{
		Rdb:    rdb,
		Rueidi: rueidica.NewRueidica(rdb),

		QueueSlugs: make(chan common.Address, 1024),

		eventHub: newEventHub(),
		// DegenDB:  degendb.NewDegenDB(),
	}

	return gb
}

func (gb *Gloomberg) SendSlugsToServer() {
	// to enable multiple users to use the central gloomberg instance for events from opensea,
	// we first send the slugs of 'our' collections to the events-subscriptions channel.
	// the central gloomberg instance then creates a subscription on the opensea
	// api and publishes upcoming incoming events to the pubsub channel
	// marshal event to json
	slugs := gb.CollectionDB.OpenseaSlugs()
	if len(slugs) == 0 {
		gbl.Log.Warn("❌ no slugs to send to gloomberg server")

		return
	}

	log.Debugf("📢 sending %s collection slugs to gloomberg server", style.BoldStyle.Render(fmt.Sprint(len(slugs))))

	mgmtEvent := &models.MgmtEvent{Action: models.Subscribe, Slugs: slugs}

	jsonMgmtEvent, err := json.Marshal(mgmtEvent)
	if err != nil {
		gbl.Log.Error("❌ marshal failed for outgoing list of collection slugs: %s | %v", err, gb.CollectionDB.OpenseaSlugs())

		return
	}

	if gb.Rdb.Do(context.Background(), gb.Rdb.B().Publish().Channel(internal.TopicSeaWatcherMgmt).Message(string(jsonMgmtEvent)).Build()).Error() != nil {
		gbl.Log.Warnf("error publishing event to redis: %s", err.Error())
	} else {
		gbl.Log.Infof("📢 sent %s collection slugs to %s", style.BoldStyle.Render(fmt.Sprint(len(slugs))), style.BoldStyle.Render(internal.TopicSeaWatcherMgmt))
	}
}

// Pr prints messages from gloomberg to the terminal.
func (gb *Gloomberg) Pr(message string) {
	gb.printToTerminal("🧃", style.Gray5Style.Render("gb"), message) // style.PinkBoldStyle.Render("・"))
}

func (gb *Gloomberg) PrWarn(message string) {
	gb.printToTerminal("⚠️", "", message)
}

func (gb *Gloomberg) PrWithKeywordAndIcon(icon string, keyword string, message string) {
	gb.printToTerminal(icon, keyword, message)
}

func (gb *Gloomberg) PrMod(mod string, message string) {
	var icon, keyword string

	if prConfig, ok := prConigs[mod]; ok {
		icon = prConfig.Icon

		if prConfig.Color != "" {
			keyword = lipgloss.NewStyle().Foreground(prConfig.Color).Render(prConfig.Keyword)
		} else {
			keyword = prConfig.Keyword
		}
	}

	gb.printToTerminal(icon, keyword, message)
}

func (gb *Gloomberg) printToTerminal(icon string, keyword string, message string) {
	if message == "" {
		return
	}

	// WEN...??
	now := time.Now()
	currentTime := now.Format("15:04:05")

	out := strings.Builder{}
	out.WriteString(style.DarkGrayStyle.Render("|"))
	out.WriteString(style.Gray4Style.Render(currentTime))
	out.WriteString(" " + lipgloss.NewStyle().MaxWidth(4).Render(icon))
	out.WriteString("  " + lipgloss.NewStyle().Width(7).Align(lipgloss.Center).Render(keyword))
	out.WriteString(" " + message)

	gb.In.PrintToTerminal <- out.String()
}

func getRedisClient() rueidis.Client {
	// use hostname as client name
	hostname, err := os.Hostname()
	if err != nil {
		log.Error(fmt.Sprintf("❗️ error getting hostname: %s", err))

		hostname = "unknown"
	}

	// rueidis / new redis library
	var connectAddr string

	if viper.IsSet("redis.address") {
		connectAddr = viper.GetString("redis.address")
	} else {
		// fallback to old config
		connectAddr = fmt.Sprintf("%s:%d", viper.GetString("redis.host"), viper.GetInt("redis.port"))
	}

	rdb, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{connectAddr}, ClientName: hostname})
	if err != nil {
		log.Fatal(err)
	}

	return rdb
}
