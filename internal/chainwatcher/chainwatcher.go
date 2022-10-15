package chainwatcher

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benleb/gloomberg/internal/cache"
	"github.com/benleb/gloomberg/internal/collections"
	"github.com/benleb/gloomberg/internal/external"
	"github.com/benleb/gloomberg/internal/models"
	"github.com/benleb/gloomberg/internal/models/topic"
	"github.com/benleb/gloomberg/internal/models/transactioncollector"
	"github.com/benleb/gloomberg/internal/nodes"
	"github.com/benleb/gloomberg/internal/utils/gbl"
	"github.com/benleb/gloomberg/internal/ws"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/spf13/viper"
)

type ChainWatcher struct {
	Nodes *nodes.Nodes

	CollectionDB *collections.CollectionDB
	// CollectionDB   *collections.CollectionDB

	queueLogs  *chan types.Log
	queueOutWS *chan *collections.Event

	WebsocketsServer *ws.WebsocketsServer
}

var (
	mu                    = &sync.Mutex{}
	knownTransactions     = make(map[common.Hash][]int)
	transactionCollectors = make(map[common.Hash]*transactioncollector.TransactionCollector)
	zeroAddress           = common.HexToAddress("0x0000000000000000000000000000000000000000")
)

func New(nodes *nodes.Nodes, collectiondb *collections.CollectionDB) *ChainWatcher {
	// create a queue/channel for the received logs
	queueLogs := make(chan types.Log, 1024)
	// create a queue/channel for events to be sent out via ws
	queueOutWS := make(chan *collections.Event, 1024)

	return &ChainWatcher{
		CollectionDB: collectiondb,
		Nodes:        nodes,

		queueLogs:  &queueLogs,
		queueOutWS: &queueOutWS,
	}
}

func (cw *ChainWatcher) SubscribeToSales(queueEvents *chan *collections.Event) {
	for _, node := range *cw.Nodes {
		gbl.Log.Debugf("%s: subscribing to chain events | QueueEvents: %d", node.Name, len(*queueEvents))

		// subscribe to all events where first topic is the "Transfer" topic
		if _, err := node.SubscribeToTransfers(*cw.queueLogs); err != nil {
			gbl.Log.Warnf("Transfers subscribe to %s failed: %s", node.WebsocketsEndpoint, err)
		}
		// subscribe to all events where first topic is the "SingleTransfer" topic
		if _, err := node.SubscribeToSingleTransfers(*cw.queueLogs); err != nil {
			gbl.Log.Warnf("SingleTransfers subscribe to %s failed: %s", node.WebsocketsEndpoint, err)
		}

		// create a defined number of workers/handlers per cNode to receive and process incoming events/logs
		for workerID := 1; workerID <= viper.GetInt("server.workers.subscription_logs"); workerID++ {
			go cw.logHandler(node, queueEvents)
		}
	}
}

func (cw *ChainWatcher) logHandler(node *nodes.Node, queueEvents *chan *collections.Event) {
	// process new logs received via our subscriptions
	for subLog := range *cw.queueLogs {
		// track & count
		nanoNow := time.Now().UnixNano()
		// // total
		// atomic.AddUint64(&totalNumReceived, 1)
		// atomic.StoreInt64(&totalLastReceived, nanoNow)
		// per nodes
		atomic.AddUint64(&node.NumLogsReceived, 1)
		atomic.StoreInt64(&node.LastLogReceived, nanoNow)

		// // TODO: trigger (yet to be implemented) contract- & walletwatcher here
		// if subLog.Address == common.HexToAddress("0x042874309Bf3F6C8E69Be4bf3D251fE9e41CF0d2") {
		// 	fmt.Println("")
		// 	fmt.Println("")
		// 	fmt.Println(" ‼️ 🤳 Impostergram 💄 Impostergram 🤳 Impostergram 💄 Impostergram 🤳 ‼️")
		// 	fmt.Println("")
		// 	fmt.Println("  https://foundation.app/collection/impostergram")
		// 	fmt.Println("")
		// 	fmt.Println("")
		// 	// notifications.SendAlert("Impostergram 🤳 💄", "https://foundation.app/collection/impostergram", true)
		// 	if msg, err := notifications.SendTelegramMessage(1320669206, " @benleb ‼️ 🤳 Impostergram 💄 Impostergram 🤳 Impostergram 💄 Impostergram 🤳 !!\n\nhttps://foundation.app/collection/impostergram", ""); err != nil {
		// 		gbl.Log.Errorf("failed to send telegram message: %v | imageURI: %s | err: %s", msg, "", err)
		// 	}
		// }

		// erc20: topics 0-2 | erc721/1155: 0-3
		if len(subLog.Topics) != 4 {
			gbl.Log.Debugf("🗑️ number of topics in log is %d (!= 4) | %v | TxHash: %v / %d | %+v", len(subLog.Topics), subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
			continue
		}

		go cw.logParser(node.NodeID, subLog, queueEvents)
	}
}

func (cw *ChainWatcher) logParser(nodeID int, subLog types.Log, queueEvents *chan *collections.Event) {
	// we use a "transaction collector" to "recognize" (wait for) multi-item tx logs
	var transco *transactioncollector.TransactionCollector

	mu.Lock()

	// check if we already have a collector for this tx hash
	if tc := transactionCollectors[subLog.TxHash]; tc != nil {
		// if we have a collector, we can add this log/logindex to the collector
		tc.AddLog(&subLog)
		mu.Unlock()
		// and return
		return
	}

	// if we don't have a collector, we create a new one for this tx hash
	transco = transactioncollector.NewTransactionCollector(&subLog)
	transactionCollectors[subLog.TxHash] = transco

	mu.Unlock()

	// wait for all logs of this tx to be received
	time.Sleep(100 * time.Millisecond)

	//
	// check if we have seen this logIndex for this transaction before
	logIndex := int(subLog.Index)

	mu.Lock()

	// check if the log is already known to us
	for _, lidx := range knownTransactions[subLog.TxHash] {
		if lidx == logIndex {
			mu.Unlock()
			return
		}
	}

	// if we don't have this logIndex, we add it to the list of known logs for this tx
	knownTransactions[subLog.TxHash] = append(knownTransactions[subLog.TxHash], logIndex)

	mu.Unlock()

	//
	// collection information
	cw.CollectionDB.RWMu.RLock()
	collection := cw.CollectionDB.Collections[subLog.Address]
	cw.CollectionDB.RWMu.RUnlock()

	// parse tx topics
	logTopic, fromAddress, toAddress, tokenID := parseTopics(subLog.Topics)

	// var txData []byte

	if collection == nil && subLog.Address != common.HexToAddress("0x0000000000000000000000000000000000000000") {
		name := ""

		if logTopic == topic.TransferSingle {
			if tokenName, err := cw.Nodes.GetRandomNode().GetERC1155TokenName(subLog.Address, tokenID); err == nil && tokenName != "" {
				name = tokenName
				gbl.Log.Debugf("found token name: %s | %s", name, subLog.Address.String())
			} else if err != nil {
				gbl.Log.Debugf("failed to get collection name: %s", err)
			}
		}

		collection = collections.NewCollection(subLog.Address, name, cw.Nodes, models.FromStream)

		cw.CollectionDB.RWMu.Lock()
		cw.CollectionDB.Collections[subLog.Address] = collection
		cw.CollectionDB.RWMu.Unlock()

		if collection == nil {
			// atomic.AddUint64(&StatsBTV.DiscardedUnknownCollection, 1)
			gbl.Log.Warnf("🗑️ collection is nil | cw.CollectionDB.UserCollections[subLog.Address] -> %v | %v | TxHash: %v / %d | %+v", cw.CollectionDB.Collections[subLog.Address], subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
			return
		}
	}

	// create a collection name if we can't find one
	if collection.Name == "" {
		preSuffix := collection.StyleSecondary().Copy().Faint(true).Render("??")
		name := collection.Style().Copy().Faint(true).Italic(true).Render("Unknown " + logTopic.String())
		collection.Name = preSuffix + " " + name + " " + preSuffix
	}

	// further (tx) information as booleans
	isMint := fromAddress == zeroAddress
	showMints := viper.GetBool("show.mints") || collection.Show.Mints
	isOwnCollection := collection.Source == models.FromWallet || collection.Source == models.FromConfiguration

	// value is just fetched for sales, not for mints
	value := big.NewInt(0)
	// cost = gas * gasPrice + value
	// cost := big.NewInt(0)

	var eventType collections.EventType

	if isMint {
		eventType = collections.Mint

		if !showMints {
			// atomic.AddUint64(&StatsBTV.DiscardedMints, 1)
			gbl.Log.Debugf("🗑️ showMints -> %v | collection.Show.Mints -> %v | %v | TxHash: %v / %d | %+v", showMints, collection.Show.Mints, subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
			return
		}
	} else {
		eventType = collections.Sale

		// get the transaction details - we don't do this for mints to save a lot of calls
		tx, _, err := cw.Nodes.GetRandomNode().Client.TransactionByHash(context.Background(), subLog.TxHash)
		if err != nil {
			gbl.Log.Debugf("🗑️ getting tx details failed | %v | TxHash: %v / %d | %+v", subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
			// atomic.AddUint64(&StatsBTV.DiscardedTransactions, 1)
			return
		}

		// cost = tx.Cost()

		// fmt.Println()
		// fmt.Printf("cost: %v\n", tx.Cost())

		// mb, _ := tx.MarshalBinary()
		// mj, _ := tx.MarshalJSON()
		// fmt.Printf("tx.MarshalBinary(): %s\n", mb)
		// fmt.Printf("tx.MarshalJSON(): %s\n", mj)

		// fmt.Println()

		// set to actual tx value
		value = tx.Value()
		// value = tx.Cost()
	}

	// if the tx has no 'value' (and is not a mint) it is a transfer
	isTransfer := value.Cmp(big.NewInt(0)) == 0 && !isMint // && logTopic != topic.TransferSingle
	showTransfers := viper.GetBool("show.transfers") || collection.Show.Transfers

	if !isMint && !isOwnCollection && nodes.WeiToEther(value).Cmp(big.NewFloat(viper.GetFloat64("show.min_value"))) < 0 {
		// atomic.AddUint64(&StatsBTV.DiscardedLowPrice, 1)
		gbl.Log.Debugf("‼️ DiscardedLowPrice| %v | TxHash: %v / %d | %+v", subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
		return
	}

	if isTransfer {
		eventType = collections.Transfer

		if !showTransfers {
			// atomic.AddUint64(&StatsBTV.DiscardedTransfers, 1)
			gbl.Log.Debugf("‼️ transfer not shown %v | TxHash: %v / %d | %+v", subLog.Address.String(), subLog.TxHash, subLog.TxIndex, subLog)
			return
		}
	}

	// if its an ENS nft, we try to get the name from the ens metadata service
	var ensMetadata *external.ENSMetadata = nil

	if collection.ContractAddress == external.ENSContract {
		// set custom collection name
		collection.Name = "ENS"

		// get ens token metadata
		metadata, err := external.GetENSMetadataForTokenID(tokenID)
		if err == nil && metadata != nil {
			ensMetadata = metadata
		} else {
			gbl.Log.Debugf("getting ens metadata failed: %s | %v", fmt.Sprint(tokenID), err)
		}
	}

	gbl.Log.Infof("")
	gbl.Log.Infof("tokenID: %s", tokenID)

	if logTopic == topic.TransferSingle {
		if tID := cw.Nodes.GetERC1155TokenID(subLog.Address, subLog.Data); tID != nil {
			gbl.Log.Infof("try tID for tokenID: %s", tokenID)
			tokenID = tID
			gbl.Log.Infof("got tID for tokenID: %s", tID)
		}
	}

	gbl.Log.Infof("")

	// if collection.SupportedStandards.Contains(standard.ERC1155) { // && (tokenID.Cmp(big.NewInt(0)) > 0 && tokenID.Cmp(big.NewInt(999_999)) < 0) {
	// 	if tID := cw.Nodes.GetERC1155TokenID(subLog.Address, subLog.Data); tID != nil {
	// 		tokenID = tID
	// 	}
	// }

	event := &collections.Event{
		NodeID:          nodeID,
		EventType:       eventType,
		Topic:           logTopic.String(),
		TxHash:          subLog.TxHash,
		Collection:      collection,
		ContractAddress: collection.ContractAddress,
		TokenID:         tokenID,
		ENSMetadata:     ensMetadata,
		PriceWei:        value,
		TxItemCount:     transco.UniqueTokenIDs(),
		Time:            time.Now(),
		From: collections.User{
			Address:       fromAddress,
			OpenseaUserID: "",
		},
		To: collections.User{
			Address:       toAddress,
			OpenseaUserID: "",
		},
	}

	// send to formatting
	*queueEvents <- event

	gbCache := cache.New()
	gbCache.StoreEvent(event.Collection.ContractAddress, event.Collection.Name, event.TokenID, event.PriceWei.Uint64(), event.TxItemCount, event.Time, int64(eventType))
}

func parseTopics(topics []common.Hash) (topic.Topic, common.Address, common.Address, *big.Int) {
	logTopic := topic.Topic(topics[0].Hex())

	// parse from/to addresses
	var fromAddress, toAddress common.Address
	if logTopic == topic.Transfer {
		fromAddress = common.HexToAddress(topics[1].Hex())
		toAddress = common.HexToAddress(topics[2].Hex())
	} else if logTopic == topic.TransferSingle {
		fromAddress = common.HexToAddress(topics[2].Hex())
		toAddress = common.HexToAddress(topics[3].Hex())
	}

	// parse token id
	rawTokenID := big.NewInt(0)
	if len(topics) >= 4 {
		rawTokenID = topics[3].Big()
	}

	return logTopic, fromAddress, toAddress, rawTokenID
}
