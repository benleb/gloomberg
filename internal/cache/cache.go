package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/benleb/gloomberg/internal/gbl"
	"github.com/charmbracelet/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

var gbCache *GbCache

const noENSName = "NO-ENS-NAME"

type GbCache struct {
	rdb *redis.Client

	mu *sync.RWMutex

	// addressToName map[common.Address]string

	localCache      map[string]string
	localFloatCache map[string]float64
}

func Initialize() *GbCache {
	// init redis client
	rdb := redis.NewClient(&redis.Options{
		Addr: strings.Join([]string{
			viper.GetString("redis.host"),
			fmt.Sprint(viper.GetInt("redis.port")),
		}, ":"),
		Password: viper.GetString("redis.password"),
		DB:       viper.GetInt("redis.database"),
	}).WithContext(context.Background())

	if gbCache != nil {
		log.Warn("cache already initialized")
	}

	gbCache = &GbCache{
		rdb: rdb,

		mu: &sync.RWMutex{},

		localCache:      make(map[string]string),
		localFloatCache: make(map[string]float64),
	}

	return gbCache
}

func GetCache() *GbCache {
	if gbCache != nil {
		return gbCache
	}

	gbCache = Initialize()

	if gbCache != nil {
		return gbCache
	}

	return nil
}

func (c *GbCache) GetRDB() *redis.Client {
	return c.rdb
}

func (c *GbCache) CacheCollectionName(collectionAddress common.Address, collectionName string) {
	c.cacheName(context.TODO(), collectionAddress, keyContract, collectionName, viper.GetDuration("cache.names_ttl"))
}

func (c *GbCache) GetCollectionName(collectionAddress common.Address) (string, error) {
	return c.getName(context.TODO(), collectionAddress, keyContract)
}

func (c *GbCache) CacheENSName(walletAddress common.Address, ensName string) {
	c.cacheName(context.TODO(), walletAddress, keyENS, ensName, viper.GetDuration("cache.ens_ttl"))
}

func (c *GbCache) GetENSName(walletAddress common.Address) (string, error) {
	return c.getName(context.TODO(), walletAddress, keyENS)
}

func (c *GbCache) cacheName(ctx context.Context, address common.Address, keyFunc func(common.Address) string, value string, duration time.Duration) {
	if value == "" {
		value = noENSName
	}

	c.mu.Lock()
	// c.addressToName[address] = value
	c.localCache[keyFunc(address)] = value
	c.mu.Unlock()

	if c.rdb != nil {
		gbl.Log.Debugf("redis | caching %s -> %s", keyFunc(address), value)

		err := c.rdb.SetEX(ctx, keyFunc(address), value, duration).Err()

		if err != nil {
			gbl.Log.Warnf("redis | error while adding: %s", err.Error())
		} else {
			gbl.Log.Debugf("redis | added: %s -> %s", keyFunc(address), value)
		}
	}
}

func (c *GbCache) getName(ctx context.Context, address common.Address, keyFunc func(common.Address) string) (string, error) {
	c.mu.RLock()
	// name := c.addressToName[address]
	name := c.localCache[keyFunc(address)]
	c.mu.RUnlock()

	if name != "" {
		if name == noENSName {
			name = ""
		}

		gbl.Log.Debugf("cache | found name in in-memory cache: '%s'", name)

		return name, nil
	}

	if c.rdb != nil {
		gbl.Log.Debugf("redis | searching for: %s", keyFunc(address))

		name, err := c.rdb.Get(ctx, keyFunc(address)).Result()

		switch {
		case errors.Is(err, nil):
			gbl.Log.Debugf("redis | using cached name: %s", name)

			c.mu.Lock()
			// c.addressToName[address] = name
			c.localCache[keyFunc(address)] = name
			c.mu.Unlock()

			if name == noENSName {
				name = ""
			}

			return name, nil

		case errors.Is(err, redis.Nil):
			gbl.Log.Debugf("redis | redis.Nil - name not found in cache: %s", keyFunc(address))

		default:
			gbl.Log.Debugf("redis | get error: %s", err)

			return "", err
		}

		// if name, err := c.rdb.Get(c.rdb.Context(), keyFunc(address)).Result(); err == nil {
		// 	gbl.Log.Debugf("redis | using cached name: %s", name)

		// 	c.mu.Lock()
		// 	// c.addressToName[address] = name
		// 	c.localCache[keyFunc(address)] = name
		// 	c.mu.Unlock()

		// 	if name == noENSName {
		// 		name = ""
		// 	}

		// 	return name, nil
		// } else if errors.Is(err, redis.Nil) {
		// 	gbl.Log.Debugf("redis | redis.Nil - name not found in cache: %s", keyFunc(address))
		// } else {
		// 	gbl.Log.Debugf("redis | get error: %s", err)

		// 	return "", err
		// }
	}

	return "", errors.New("name not found in cache")
}

func (c *GbCache) cacheFloat(ctx context.Context, address common.Address, keyFunc func(common.Address) string, value float64, duration time.Duration) {
	c.mu.Lock()
	// c.addressToName[address] = value
	c.localFloatCache[keyFunc(address)] = value
	c.mu.Unlock()

	if c.rdb != nil {
		gbl.Log.Debugf("redis | caching %s -> %f", keyFunc(address), value)

		err := c.rdb.SetEX(ctx, keyFunc(address), value, duration).Err()

		if err != nil {
			gbl.Log.Warnf("redis | error while adding: %s", err.Error())
		} else {
			gbl.Log.Debugf("redis | added: %s -> %f", keyFunc(address), value)
		}
	}
}

func (c *GbCache) getFloat(ctx context.Context, address common.Address, keyFunc func(common.Address) string) (float64, error) {
	c.mu.RLock()
	// value := c.addressToName[address]
	value := c.localFloatCache[keyFunc(address)]
	c.mu.RUnlock()

	if value != 0 {
		gbl.Log.Debugf("cache | found name in in-memory cache: '%f'", value)

		return value, nil
	}

	if c.rdb != nil {
		gbl.Log.Debugf("redis | searching for: %s", keyFunc(address))

		value, err := c.rdb.Get(ctx, keyFunc(address)).Float64()

		switch {
		case errors.Is(err, nil):
			gbl.Log.Debugf("redis | using cached value: %f", value)

			c.mu.Lock()
			c.localFloatCache[keyFunc(address)] = value
			c.mu.Unlock()

			return value, nil

		case errors.Is(err, redis.Nil):
			gbl.Log.Debugf("redis | redis.Nil - value not found in cache: %s", keyFunc(address))

		default:
			gbl.Log.Debugf("redis | get error: %s", err)

			return 0, err
		}
	}

	return 0, errors.New("value not found in cache")
}

// names.
func StoreENSName(ctx context.Context, walletAddress common.Address, ensName string) {
	c := GetCache()
	c.cacheName(ctx, walletAddress, keyENS, ensName, viper.GetDuration("cache.ens_ttl"))
}

func GetENSName(ctx context.Context, walletAddress common.Address) (string, error) {
	c := GetCache()

	return c.getName(ctx, walletAddress, keyENS)
}

func StoreContractName(ctx context.Context, contractAddress common.Address, contractName string) {
	c := GetCache()

	c.cacheName(ctx, contractAddress, keyContract, contractName, viper.GetDuration("cache.names_ttl"))
}

func GetContractName(ctx context.Context, contractAddress common.Address) (string, error) {
	c := GetCache()

	return c.getName(ctx, contractAddress, keyContract)
}

// slugs.
func StoreOSSlug(ctx context.Context, contractAddress common.Address, slug string) {
	c := GetCache()

	c.cacheName(ctx, contractAddress, keyOSSlug, slug, viper.GetDuration("cache.slug_ttl"))
}

func StoreBlurSlug(ctx context.Context, contractAddress common.Address, slug string) {
	c := GetCache()

	c.cacheName(ctx, contractAddress, keyBlurSlug, slug, viper.GetDuration("cache.slug_ttl"))
}

// numbers.
func StoreFloor(ctx context.Context, address common.Address, value float64) {
	c := GetCache()

	c.cacheFloat(ctx, address, keyFloorPrice, value, viper.GetDuration("cache.floor_ttl"))
}

func GetFloor(ctx context.Context, address common.Address) (float64, error) {
	c := GetCache()

	return c.getFloat(ctx, address, keyFloorPrice)
}

func StoreSalira(ctx context.Context, address common.Address, value float64) {
	c := GetCache()

	c.cacheFloat(ctx, address, keySalira, value, viper.GetDuration("cache.salira_ttl"))
}

func GetSalira(ctx context.Context, address common.Address) (float64, error) {
	c := GetCache()

	return c.getFloat(ctx, address, keySalira)
}

// NotificationLock implements a lock to prevent sending multiple notifications for the same event
// see https://redis.io/docs/manual/patterns/distributed-locks/#correct-implementation-with-a-single-instance
func NotificationLock(ctx context.Context, txID common.Hash) (bool, error) {
	c := GetCache()

	releaseKey := uuid.New()

	c.mu.Lock()
	c.localCache[keyNotificationsLock(txID)] = releaseKey.String()
	c.mu.Unlock()

	unlocked := false

	var err error

	if c.rdb != nil {
		unlocked, err = c.rdb.SetNX(ctx, keyNotificationsLock(txID), releaseKey.String(), viper.GetDuration("cache.notifications_lock_ttl")).Result()

		gbl.Log.Debugf("📣 %s | locked %+v", txID.String(), unlocked)

		if err != nil {
			gbl.Log.Warnf("❌ redis | error while adding: %s", err.Error())
		} else {
			gbl.Log.Debugf("📣 redis | added: %s -> %s", keyNotificationsLock(txID), releaseKey)
		}
	}

	return unlocked, nil
}

func ReleaseNotificationLock(ctx context.Context, contractAddress common.Address) (string, error) {
	c := GetCache()

	return c.getName(ctx, contractAddress, keyContract)
}
