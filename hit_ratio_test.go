package mcache

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// TestTrashingResistance test cache hit ratio for the case when the cache
// doesn't fit the data being cached. The user of the cache keeps reading and
// replacing items on the cache, what could cause LRU cache to have a hit ratio
// equals to 0, becoming useless. This test asserts that this will not happen.
func TestTrashingResistance(t *testing.T) {
	const maxItemsOnCache = 8
	data := make([]byte, 0)
	exp := time.Now().Add(time.Hour)
	rand.Seed(0)

	cacheSize := maxItemsOnCache * memUsageOnCache("000000", []byte{})
	t.Logf("cache size: %d bytes", cacheSize)
	c := New(cacheSize)

	for i := 0; i < maxItemsOnCache; i++ {
		key := fmt.Sprintf("%06d", i)
		c.Put(key, data, exp)
	}

	t.Logf("After preload:\n cold=%v\n hot=%v", &c.cold, &c.hot)

	// simulating a pattern where an item is searched on cache, and if it is
	// not found, the cache gets populated. But there are 10 times more items
	// then the cache can hold.
	hits := 0
	for cycle := 0; cycle < 2; cycle++ {
		for i := 0; i < 10*maxItemsOnCache; i++ { // FIXME change to 10*
			key := fmt.Sprintf("%06d", i)
			_, hit := c.Get(key)
			if hit {
				hits++
			} else {
				c.Put(key, data, exp)
			}
		}
	}

	t.Logf("Hits: %d, statistics: %s", hits, c.Statistics())
}
