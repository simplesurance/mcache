package mcache

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/simplesurance/mcache/rndmap"
)

// New creates a new cache instance with the give memory limit.
func New(memLim int64, opt ...Option) *Cache {
	ret := Cache{
		settings: applyOptions(opt...),
		hot: cacheArea{
			maxSize: memLim / 2,
			items:   rndmap.IndexableMap[string, *item]{},
			mu:      &sync.RWMutex{},
		},
		cold: cacheArea{
			maxSize:     memLim / 2,
			items:       rndmap.IndexableMap[string, *item]{},
			mu:          &sync.RWMutex{},
			fastAndDump: true,
		},
	}

	return &ret
}

// Cache can store and retrieved stored objects, while enforcing memory limit
// and optional per-item TTL.
type Cache struct {
	settings *settings
	hot      cacheArea
	cold     cacheArea // avoid deadlock: if locking "hot" and "cold", need to do it in this order
	stats    struct {
		hits   int64
		misses int64
		writes int64
	}
}

// Get retrieves a cached item, if an unexpired one can be found.
func (c *Cache) Get(key string) ([]byte, bool) {
	// the order "hot", then "cold" must be followed to avoid deadlocks
	now := time.Now()
	i := 0
	for _, area := range []*cacheArea{&c.hot, &c.cold} {
		i++
		area.mu.RLock()
		defer func(area *cacheArea) {
			area.mu.RUnlock()
		}(area)

		it, ok := area.items.GetByKey(key)
		if ok {
			if now.After(it.expiration) {
				// not allowed to delete: have only a read lock
				atomic.AddInt64(&c.stats.misses, 1)
				return nil, false
			}

			atomic.AddInt64(&it.accessCount, 1)
			atomic.AddInt64(&c.stats.hits, 1)
			return it.value, true
		}
	}

	atomic.AddInt64(&c.stats.misses, 1)
	return nil, false
}

// Put stores one item on cache.
func (c *Cache) Put(key string, value []byte, exp time.Time) {
	atomic.AddInt64(&c.stats.writes, 1)

	size := memUsageOnCache(key, value)
	if size > c.cold.maxSize/2 {
		// At least a few items of this size must fit on a cache are, or
		// else the promotion to the "hot" area won't be possible. Picking
		// a size limit that would allow at least 2 items to fit on a cache
		// area.
		return
	}

	it := item{
		value:      value,
		stored:     time.Now(),
		expiration: exp,
	}

	// The item might already be present in the hot cache. For that it is
	// necessary to promote the lock from ro to rw, then recompute the
	// change in memory. If the new item is bigger then the old one, there
	// might be a spillover from the hot cache to the cold one.
	c.hot.mu.RLock()
	if _, exists := c.hot.items.GetByKey(key); exists {
		c.hot.mu.RUnlock()
		c.hot.mu.Lock()
		defer c.hot.mu.Unlock()

		// FIXME: check again if key was added it was unlocked

		spilover := map[string]*item{}
		put(&c.hot, key, &it, spilover)

		if len(spilover) == 0 {
			return
		}

		c.cold.mu.Lock()
		defer c.cold.mu.Unlock()

		for key, it := range spilover {
			put(&c.cold, key, it, nil)
		}

		return
	}

	defer c.hot.mu.RUnlock() // unlock only at the end

	c.cold.mu.Lock()
	defer c.cold.mu.Unlock()

	// new items always go to the cold cache, to avoid trashing the hot
	// area.
	put(&c.cold, key, &it, nil)
}

// Statistics loads statistics about the cache.
func (c *Cache) Statistics() Statistics {
	c.hot.mu.RLock()
	defer c.hot.mu.RUnlock()

	c.cold.mu.RLock()
	defer c.cold.mu.RUnlock()

	return Statistics{
		Writes:      atomic.LoadInt64(&c.stats.writes),
		Hits:        atomic.LoadInt64(&c.stats.hits),
		Misses:      atomic.LoadInt64(&c.stats.misses),
		MemoryUsage: c.hot.memoryUsage + c.cold.memoryUsage,
		HotItems:    c.hot.items.Size(),
		ColdItems:   c.cold.items.Size(),
	}
}

// Statistics hods statistic data about the cache.
type Statistics struct {
	Writes      int64
	Hits        int64
	Misses      int64
	MemoryUsage int64
	HotItems    int
	ColdItems   int
}

func (s Statistics) String() string {
	return fmt.Sprintf(
		"%d writes, %d/%d hits (%f%%), mem usage=%d, items=%d/%d (hot/cold)",
		s.Writes, s.Hits, s.Hits+s.Misses,
		100*float64(s.Hits)/float64(s.Hits+s.Misses),
		s.MemoryUsage, s.HotItems, s.ColdItems)
}

type cacheArea struct {
	mu          *sync.RWMutex
	items       rndmap.IndexableMap[string, *item]
	memoryUsage int64
	maxSize     int64
	fastAndDump bool // cache eviction is O(1), but very dump
}

type item struct {
	value       []byte
	stored      time.Time
	accessCount int64
	expiration  time.Time
}

func (it *item) usefullness(key string) float64 {
	if it.expiration.Before(time.Now()) {
		return 0
	}

	age := float64(time.Since(it.stored).Seconds())
	size := float64(memUsageOnCache(key, it.value))
	accesses := float64(atomic.LoadInt64(&it.accessCount))
	return accesses / (age * size)
}

func put(area *cacheArea, key string, it *item, spilover map[string]*item) {
	remove(area, key)

	size := memUsageOnCache(key, it.value)

	// New items go to the cold cache to avoid trashing the hot cache.
	// Make space for the new item on the cold cache.
	limitMemoryUsage(area, area.maxSize-size, spilover)
	if area.maxSize-area.memoryUsage < size {
		// it doesn't fit
		return
	}

	area.memoryUsage += size
	area.items.Put(key, it)

}

// keep items that have the highest value of:
//
//	accesses/(age*size)
//
// the value of access/age is an estimation of how many cache hits per unit
// of time are expected to happen if the item is kept on cache. The size of
// the item is also included on the metric because:
//   - not having the item on cache is assumed to result in the need of
//     a network request, which is typically latency-dominant,  meaning that
//     unless the object is very big compared to the network badwidth, its
//     size is not that important as a metric
//   - an object that has double the size uses exactly double the memory.
func (c *Cache) reorganize() {
	cleanExpired(&c.cold)

	// Hot items normally can occupy up to 1/2 of the memory allocated to the
	// cache. Free up 1/4 of that for taking items from the cold cache.
	spilover := map[string]*item{}
	limitMemoryUsage(&c.hot, 3*c.settings.maxMemory/8, spilover)
}

func limitMemoryUsage(area *cacheArea, maxMem int64, spilover map[string]*item) bool {
	if maxMem < 0 {
		return false // impossible to accomplish
	}

	if area.memoryUsage <= maxMem {
		return true // there is already enough space
	}

	cleanExpired(area)
	if area.memoryUsage <= maxMem {
		return true // there is already enough space
	}

	if area.fastAndDump {
		for {
			rndix := rand.Intn(area.items.Size())
			key, _ := area.items.GetByIndex(rndix)
			remove(area, key)
			if area.memoryUsage <= maxMem {
				return true // there is already enough space
			}
		}
	}

	// slow cleanup
	for _, victim := range sortedKeys(area) {
		key := victim.key
		if area.memoryUsage <= maxMem {
			return true
		}

		if spilover != nil {
			spilover[key], _ = area.items.GetByKey(key)
		}

		remove(area, key)

		if area.memoryUsage <= maxMem {
			return true // there is already enough space
		}
	}

	return false // should not happen
}

func cleanExpired(area *cacheArea) {
	items := area.items.Items()
	now := time.Now()
	for {
		key, it, ok := items()
		if !ok {
			break
		}

		if it.expiration.Before(now) {
			remove(area, key)
		}
	}
}

// removes removes the item from the cache, update memory consumption and
// return old item.
// - area must be RW-locked
func remove(area *cacheArea, key string) {
	it, ok := area.items.GetByKey(key)
	if !ok {
		return
	}

	area.memoryUsage -= memUsageOnCache(key, it.value)
	area.items.DeleteByKey(key)
}

func sortedKeys(area *cacheArea) []selectedKey {
	ret := make([]selectedKey, area.items.Size())

	var ix = 0
	items := area.items.Items()
	for {
		key, it, ok := items()
		if !ok {
			break
		}

		ret[ix] = selectedKey{
			usefullness: it.usefullness(key),
			key:         key,
		}
		ix++
	}

	sort.Slice(ret, func(i, j int) bool {
		return ret[i].usefullness < ret[j].usefullness
	})

	return ret
}

type selectedKey struct {
	key         string
	usefullness float64
}

// memUsageOnCache is a rough approximation of the memory used to store an
// item.
func memUsageOnCache(key string, val []byte) int64 {
	keyMemUsage := 8 + len(key)
	valMemUsage := 12 + len(val)
	sliceOverhead := 8
	size := int64(valMemUsage + keyMemUsage + sliceOverhead)
	return size
}
