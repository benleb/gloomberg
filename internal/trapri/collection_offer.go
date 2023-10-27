package trapri

import (
	"math/big"
	"sync"
	"time"

	"github.com/benleb/gloomberg/internal/degendb"
	"github.com/benleb/gloomberg/internal/gbl"
	"github.com/benleb/gloomberg/internal/nemo/gloomberg"
	"github.com/benleb/gloomberg/internal/nemo/marketplace"
	"github.com/benleb/gloomberg/internal/nemo/price"
	"github.com/benleb/gloomberg/internal/nemo/token"
	"github.com/benleb/gloomberg/internal/nemo/totra"
	"github.com/benleb/gloomberg/internal/seawa/models"
	"github.com/ethereum/go-ethereum/common"
)

var (
	collectionOffers      = map[common.Address]*models.CollectionOffer{}
	collectionOffersMutex = &sync.Mutex{}
)

func HandleCollectionOffer(gb *gloomberg.Gloomberg, event *models.CollectionOffer) {
	contractAddress := common.HexToAddress(event.Payload.ContractCriteria.Address.Hex())

	// seller address
	sellerAddress := event.Payload.Maker.Address

	// parse tokenPrice
	var tokenPrice *price.Price
	if event.Payload.BasePrice != nil {
		basePrice := event.Payload.BasePrice
		if event.Payload.Quantity > 1 {
			basePrice = new(big.Int).Div(event.Payload.BasePrice, big.NewInt(int64(event.Payload.Quantity)))
		}

		tokenPrice = price.NewPrice(basePrice)
	} else {
		tokenPrice = price.NewPrice(big.NewInt(0))

		gbl.Log.Warnf("🤷‍♀️ error parsing tokenPrice: %+v", event.Payload)
	}

	// if it should be a new top bid, we highlight it when printing
	// highlight := false

	// check if we already have a top bid for this token and if not, add it
	collectionOffersMutex.Lock()
	currentTopOffer := collectionOffers[contractAddress]
	collectionOffersMutex.Unlock()

	switch {
	// no or expired top offer - setting new top offer
	case currentTopOffer == nil:
		gbl.Log.Debugf("🍭 no current top offer, new top offer: %+v", event.Payload.GetPrice().Wei())

	case currentTopOffer.Payload.ExpirationDate.Before(time.Now()):
		gbl.Log.Debugf("🍭 top offer expired, new top offer: %+v", event.Payload.GetPrice().Wei())

	// new offer is higher than current top offer
	case currentTopOffer != nil:
		// we add a small amount (still researching how much :D) of ether/wei to the current top offer before comparing
		// to prevent printing a lot of backrunned (=doubled) offers all the time
		// amountToAdd := big.NewInt(13370000000000001) // ≈0.01337....Ξ
		// amountToAdd := big.NewInt(10370000000000001) // ≈0.01037....Ξ
		// amountToAdd := big.NewInt(7370000000000001) // ≈0.00737....Ξ
		amountToAdd := big.NewInt(6666666666666666) // ≈0.00666....Ξ
		// amountToAdd := big.NewInt(3370000000000001) // ≈0.00337....Ξ

		currentTopOfferWithBuffer := big.NewInt(0).Add(currentTopOffer.Payload.GetPrice().Wei(), amountToAdd)

		eventPrice := big.NewInt(0).Div(event.Payload.GetPrice().Wei(), big.NewInt(int64(event.Payload.Quantity)))

		if eventPrice.Cmp(currentTopOfferWithBuffer) < 0 {
			gbl.Log.Debugf("🍭 current top offer (+buffer) higher than incoming bid: %+v > %+v", currentTopOfferWithBuffer, event.Payload.GetPrice().Wei())

			return
		}
	}

	// set the new top offer
	collectionOffersMutex.Lock()
	collectionOffers[contractAddress] = event
	collectionOffersMutex.Unlock()

	// get the item name as it is not
	itemName := event.Payload.CollectionCriteria.Slug
	if collection := gb.CollectionDB.GetCollectionForSlug(event.Payload.CollectionCriteria.Slug); collection != nil {
		itemName = collection.Name
	}

	// create a TokenTransaction
	ttxCollectionOffer := &totra.TokenTransaction{
		Tx:          nil,
		TxReceipt:   nil,
		From:        sellerAddress,
		AmountPaid:  tokenPrice.Wei(),
		TotalTokens: int64(event.Payload.Quantity),
		Marketplace: &marketplace.OpenSea,
		Action:      degendb.CollectionOffer,
		ReceivedAt:  event.Payload.EventTimestamp,
		DoNotPrint:  false,
		Transfers: []*totra.TokenTransfer{
			{
				From:         marketplace.OpenSea.ContractAddress(),
				To:           sellerAddress,
				AmountTokens: big.NewInt(int64(event.Payload.Quantity)),
				Token: &token.Token{
					Address: contractAddress,
					ID:      big.NewInt(0),
					Name:    itemName,
				},
			},
		},
	}

	// format and print
	gb.In.TokenTransactions <- ttxCollectionOffer
}
