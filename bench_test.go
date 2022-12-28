package mcache_test

import (
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/simplesurance/mcache"
)

func BenchmarkPut(b *testing.B) {
	c := mcache.New(10 * 1024 * 1024)
	exp := time.Now().Add(10 * time.Second)
	data := []byte{}

	for i := 0; i < b.N; i++ {
		c.Put(strconv.Itoa(i), data, exp)
	}
}

func BenchmarkGetPut(b *testing.B) {
	c := mcache.New(1024 * 1024)
	exp := time.Now().Add(10 * time.Second)
	data := []byte{}

	b.RunParallel(func(p *testing.PB) {
		for p.Next() {
			key := strconv.Itoa(rand.Intn(100000))
			if rand.Int()%5 == 0 {
				c.Put(key, data, exp)
			} else {
				c.Get(key)
			}
		}
	})

	b.Logf("Cache statistics: %v", c.Statistics())
}
