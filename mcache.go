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
		ordered: cacheArea{
			maxBytes: memLim / 2,
			items:    rndmap.IndexableMap[string, *item]{},
			mu:       &sync.RWMutex{},
		},
		random: cacheArea{
			maxBytes:    memLim / 2,
			items:       rndmap.IndexableMap[string, *item]{},
			mu:          &sync.RWMutex{},
			fastAndDumb: true,
		},
	}

	return &ret
}

// Cache can store and retrieved stored objects, while enforcing memory limit
// and optional per-item TTL.
type Cache struct {
	settings *settings
	ordered  cacheArea
	random   cacheArea // avoid deadlock: if locking "ordered" and "random", need to do it in this order
	stats    struct {
		hits   int64
		misses int64
		writes int64
	}
}

// Get retrieves a cached item, if an unexpired one can be found.
func (c *Cache) Get(key string) ([]byte, bool) {
	now := time.Now()
	i := 0
	for _, area := range []*cacheArea{&c.ordered, &c.random} {
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

// Put stores one item in cache.
func (c *Cache) Put(key string, value []byte, exp time.Time) {
	now := time.Now()
	atomic.AddInt64(&c.stats.writes, 1)

	size := memUsageOnCache(key, value)
	if size > c.random.maxBytes {
		// if the item is too big it won't be cached, and if there is already
		// a version on the cache, that item must be removed
		c.ordered.mu.Lock()
		defer c.ordered.mu.Unlock()
		if remove(&c.ordered, key) {
			return
		}

		c.random.mu.Lock()
		defer c.random.mu.Unlock()
		remove(&c.random, key)

		return
	}

	c.promoteToHotIfNeeded(size, now)

	it := item{
		value:      value,
		stored:     now,
		expiration: exp,
	}

	c.ordered.mu.RLock()
	if _, exists := c.ordered.items.GetByKey(key); exists {
		// item is already on the hot cache; lock it as rw to update in-place
		c.ordered.mu.RUnlock()
		c.ordered.mu.Lock()
		defer c.ordered.mu.Unlock()

		// FIXME: check again if key was added it was unlocked

		spilover := map[string]*item{}
		put(&c.ordered, key, &it, spilover, true, now)

		if len(spilover) == 0 {
			return
		}

		c.random.mu.Lock()
		defer c.random.mu.Unlock()

		for key, it := range spilover {
			put(&c.random, key, it, nil, true, now)
		}

		return
	}

	defer c.ordered.mu.RUnlock() // unlock only at the end

	c.random.mu.Lock()
	defer c.random.mu.Unlock()

	// new items always go to the random cache, to avoid trashing the hot
	// area.
	put(&c.random, key, &it, nil, true, now)
}

// Statistics loads statistics about the cache.
func (c *Cache) Statistics() Statistics {
	c.ordered.mu.RLock()
	defer c.ordered.mu.RUnlock()

	c.random.mu.RLock()
	defer c.random.mu.RUnlock()

	return Statistics{
		Writes:           atomic.LoadInt64(&c.stats.writes),
		Hits:             atomic.LoadInt64(&c.stats.hits),
		Misses:           atomic.LoadInt64(&c.stats.misses),
		MemoryUsage:      c.ordered.memoryUsage + c.random.memoryUsage,
		OrderedItemCount: c.ordered.items.Size(),
		RandomItemCount:  c.random.items.Size(),
	}
}

func (c *Cache) needsPromotionToFit(size int64) (int64, bool) {
	writtenSinceLastPromotion := atomic.LoadInt64(&c.random.byteWrittenSinceLastPromotion)
	needsPromotion := writtenSinceLastPromotion+size > c.random.maxBytes/4
	return writtenSinceLastPromotion, needsPromotion
}

func (c *Cache) promoteToHotIfNeeded(size int64, now time.Time) {
	if _, needs := c.needsPromotionToFit(size); !needs {
		return
	}

	c.ordered.mu.Lock()
	defer c.ordered.mu.Unlock()

	// need to repeat the test inside the critical section because the first
	// test allows parallel execution
	written, needs := c.needsPromotionToFit(size)
	if !needs {
		return
	}

	atomic.AddInt64(&c.random.byteWrittenSinceLastPromotion, -written)

	c.random.mu.Lock()
	defer c.random.mu.Unlock()

	orderedSortedKeys := sortedKeys(&c.random, now)

	//defer func(rndItems, ordItems int, rndSoFar int64) {
	//	highestPromotedKey := orderedSortedKeys[len(orderedSortedKeys)-1].key
	//	highestPromoted, _ := c.ordered.items.GetByKey(highestPromotedKey)

	//	orderedKeys := sortedKeys(&c.ordered, now)
	//	highestOverallKey := orderedKeys[len(orderedKeys)-1].key
	//	highestOverall, _ := c.ordered.items.GetByKey(highestOverallKey)

	//	fmt.Fprintf(os.Stdout,
	//		"promotion result:\n len-random=%d=>%d\n len-sequential=%d=>%d\n written=%d\n randomSoFar=%d=>%d\n highest-promoted=%v\n highest-overall=%v\n",
	//		rndItems, c.random.items.Size(),
	//		ordItems, c.ordered.items.Size(),
	//		written,
	//		rndSoFar, atomic.LoadInt64(&c.random.byteWrittenSinceLastPromotion),
	//		highestPromoted.usefullness(highestPromotedKey, now),
	//		highestOverall.usefullness(highestOverallKey, now),
	//	)
	//}(c.random.items.Size(), c.ordered.items.Size(), atomic.LoadInt64(&c.random.byteWrittenSinceLastPromotion))

	var cut int
	var bytesToPromote int64
	for cut = len(orderedSortedKeys); cut > 0; cut-- {
		itInfo := orderedSortedKeys[cut-1]
		item, _ := c.random.items.GetByKey(itInfo.key)
		itSize := memUsageOnCache(itInfo.key, item.value)

		if bytesToPromote+itSize > c.random.maxBytes/2 {
			break
		}

		bytesToPromote += itSize
	}

	toPromote := orderedSortedKeys[cut:]
	notPromoted := orderedSortedKeys[:cut]

	// make up space on the hot cache
	spilover := map[string]*item{}
	limitMemoryUsage(&c.ordered, bytesToPromote, spilover, now)

	// move items from the cold cache to the hot cache
	for _, itInfo := range toPromote {
		it, _ := c.random.items.GetByKey(itInfo.key)
		put(&c.ordered, itInfo.key, it, nil, true, now)
		remove(&c.random, itInfo.key)
	}

	// store items that spillover from hot cache into the cold cache by
	// evicting items that where not promoted
	for key, it := range spilover {
		// make space for an item
		for c.random.maxBytes-c.random.memoryUsage < memUsageOnCache(key, it.value) {
			remove(&c.random, notPromoted[0].key)
			notPromoted = notPromoted[1:]
		}

		// store it
		put(&c.random, key, it, nil, false, now)
	}
}

// Statistics hods statistic data about the cache.
type Statistics struct {
	Writes           int64
	Hits             int64
	Misses           int64
	MemoryUsage      int64
	OrderedItemCount int
	RandomItemCount  int
}

func (s Statistics) String() string {
	return fmt.Sprintf(
		"%d writes, %d/%d hits (%f%%), mem usage=%d, items=%d/%d (hot/cold)",
		s.Writes, s.Hits, s.Hits+s.Misses,
		100*float64(s.Hits)/float64(s.Hits+s.Misses),
		s.MemoryUsage, s.OrderedItemCount, s.RandomItemCount)
}

type cacheArea struct {
	mu                            *sync.RWMutex
	items                         rndmap.IndexableMap[string, *item]
	memoryUsage                   int64
	maxBytes                      int64
	fastAndDumb                   bool  // cache eviction is O(1), but very dump
	byteWrittenSinceLastPromotion int64 // atomic
}

// FIXME remove
func (c *cacheArea) String() string {
	return fmt.Sprintf("mem=%d/%d dumb=%t written=%dB items=%v",
		c.memoryUsage, c.maxBytes, c.fastAndDumb, c.byteWrittenSinceLastPromotion, &c.items)
}

type item struct {
	value       []byte
	stored      time.Time
	accessCount int64
	expiration  time.Time
}

func (it *item) usefullness(key string, now time.Time) float64 {
	if it.expiration.Before(now) {
		return 0
	}

	age := float64(time.Since(it.stored).Seconds())
	size := float64(memUsageOnCache(key, it.value))
	accesses := float64(atomic.LoadInt64(&it.accessCount))
	return accesses / (age * size)
}

func put(
	area *cacheArea,
	key string,
	it *item,
	spilover map[string]*item,
	mustAccount bool,
	now time.Time,
) {
	remove(area, key)

	size := memUsageOnCache(key, it.value)

	// New items go to the cold cache to avoid trashing the hot cache.
	// Make space for the new item on the cold cache.
	limitMemoryUsage(area, area.maxBytes-size, spilover, now)

	if mustAccount { // FIXME this is ugly! make it go away!
		atomic.AddInt64(&area.byteWrittenSinceLastPromotion, size)
	}
	area.memoryUsage += size
	area.items.Put(key, it)
}

func limitMemoryUsage(
	area *cacheArea, maxMem int64, spilover map[string]*item, now time.Time,
) bool {
	if area.memoryUsage <= maxMem {
		return true // there is already enough space
	}

	cleanExpired(area, now)
	if area.memoryUsage <= maxMem {
		return true // there is already enough space
	}

	// the cache area may allow a "fast and dump" method that allow removing
	// each item in O(1) on time and space
	if area.fastAndDumb {
		for {
			rndix := rand.Intn(area.items.Size())
			rndKey, _ := area.items.GetByIndex(rndix)

			if spilover != nil {
				spilover[rndKey], _ = area.items.GetByKey(rndKey)
			}

			remove(area, rndKey)

			if area.memoryUsage <= maxMem {
				return true // there is already enough space
			}
		}
	}

	// slow cleanup
	for _, victim := range sortedKeys(area, now) {
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

func cleanExpired(area *cacheArea, now time.Time) {
	items := area.items.Items()
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
func remove(area *cacheArea, key string) bool {
	it, ok := area.items.GetByKey(key)
	if !ok {
		return false
	}

	area.memoryUsage -= memUsageOnCache(key, it.value)
	area.items.DeleteByKey(key)
	return true
}

func sortedKeys(area *cacheArea, now time.Time) []selectedKey {
	ret := make([]selectedKey, area.items.Size())

	var ix = 0
	items := area.items.Items()
	for {
		key, it, ok := items()
		if !ok {
			break
		}

		ret[ix] = selectedKey{
			usefulness: it.usefullness(key, now),
			key:        key,
		}
		ix++
	}

	sort.Slice(ret, func(i, j int) bool {
		return ret[i].usefulness < ret[j].usefulness
	})

	return ret
}

type selectedKey struct {
	key        string
	usefulness float64
}

// FIXME remove
func (s selectedKey) String() string {
	return fmt.Sprintf("%q=%f", s.key, s.usefulness)
}

// memUsageOnCache is a rough approximation of the memory used to store an
// item.
func memUsageOnCache(key string, val []byte) int64 {
	// return 1
	return int64(len(key) + len(val) + 50)
}
