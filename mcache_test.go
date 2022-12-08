package mcache_test

import (
	"os"
	"runtime/pprof"
	"strconv"
	"testing"
	"time"

	"github.com/simplesurance/mcache"
	"golang.org/x/exp/slices"
)

func TestMCache(t *testing.T) {
	c := mcache.New(100 * 1024 * 1024)
	want := []byte("test")

	c.Put("abc", want, time.Now().Add(10*time.Second))
	stats := c.Statistics()
	t.Logf("stats: %v", stats)
	if stats.MemoryUsage == 0 {
		t.Fatalf("memory usage sill 0 after adding item to cache")
	}

	have, ok := c.Get("abc")
	if !ok {
		t.Fatalf("unable to restore item that just got added")
	}

	if slices.Compare(want, have) != 0 {
		t.Fatalf("item on cache is not what was put there")
	}
}

// TestReplace asserts then when replacing an item, the memory usage of the
// old item is being replaced by the memory usage of the new item.
func TestReplace(t *testing.T) {
	c := mcache.New(1024 * 1024)
	data1 := []byte("test")
	data2 := []byte("test data with different length")

	c.Put("abc", data1, time.Now().Add(10*time.Second))
	wantStats := c.Statistics()

	// change the value
	c.Put("abc", data2, time.Now().Add(10*time.Second))
	changedStats := c.Statistics()
	if changedStats.MemoryUsage == wantStats.MemoryUsage {
		t.Fatalf("memory usage not changed after writing item with different size: %v", changedStats)
	}
	read, ok := c.Get("abc")
	if !ok || !slices.Equal(read, data2) {
		t.Fatal("data not restored to the original value after writing the original value")
	}

	// change back - must be using the same amount of memory as before
	c.Put("abc", data1, time.Now().Add(10*time.Second))
	haveStats := c.Statistics()
	if haveStats.MemoryUsage != wantStats.MemoryUsage {
		t.Fatalf(
			"memory usage not reverted to original value after value got reverted to it: want=%d have=%d",
			wantStats.MemoryUsage, haveStats.MemoryUsage)
	}
}

func TestPutGetPerformance(t *testing.T) {
	const wantWrites = 100000
	const maxTime = 5 * time.Second

	f, err := os.Create("pprof.pb.gz")
	if err != nil {
		t.Fatalf("Error creating pprof.txt: %v", err)
	}
	defer f.Close()

	c := mcache.New(100 * 1024)
	data := make([]byte, 0)

	var i int
	exp := time.Now().Add(time.Hour)

	// generate key names outside of the code being profiled
	keys := make([]string, wantWrites)
	for i = 0; i < wantWrites; i++ {
		keys[i] = strconv.Itoa(i)
	}

	start := time.Now()

	pprof.StartCPUProfile(f)
	for i = 0; i < wantWrites; i++ {
		c.Put(keys[i], data, exp)
		c.Get("0")
	}
	pprof.StopCPUProfile()

	duration := time.Since(start)
	if duration > maxTime {
		t.Fatalf("Put executed %d times in %v; it should be finished in less then %v", i, duration, maxTime)
	}

	t.Logf("%v", c.Statistics())
}
