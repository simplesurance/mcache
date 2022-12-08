package rndmap_test

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/simplesurance/mcache/rndmap"
)

func TestPut(t *testing.T) {
	m := rndmap.IndexableMap[int, int]{}

	for i := 0; i < 100; i++ {
		m.Put(i+1, i+2)
	}

	for i := 0; i < 100; i++ {
		v, ok := m.GetByKey(i + 1)
		if !ok {
			t.Errorf("want: m[%d]=%d, but item with such index was not found", i, i+1)
		}
		if v != i+2 {
			t.Errorf("want: m[%d]=%d, have %d", i, i+1, v)
		}
	}

	for i := 0; i < 100; i++ {
		k, v := m.GetByIndex(i)
		if k != i+1 || v != i+2 {
			t.Errorf("want: m[%d]=%d, have %d", i, i+2, v)
		}
	}
}

func TestDelete(t *testing.T) {
	const max = 200
	for testv := 0; testv < max; testv++ {
		t.Run(strconv.Itoa(testv), func(t *testing.T) {
			m := rndmap.IndexableMap[int, string]{}

			for i := 0; i < max; i++ {
				m.Put(i, strconv.Itoa(i))
			}
			m.DeleteByKey(testv)

			for i := 0; i < max-1; i++ {
				v, ok := m.GetByKey(i)
				if i == testv {
					if ok {
						t.Errorf("m[ix=%d] was deleted, but still can be read", i)
					}
				} else if v != strconv.Itoa(i) {
					t.Errorf("m[ix=%d]=%s, should be %s", i, v, strconv.Itoa(i))
				}
			}
		})
	}
}

func TestMemory(t *testing.T) {
	showMemUsage := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("mem stats: stack:%d heap:%d", ms.StackInuse, ms.HeapInuse)
	}

	showMemUsage()

	const max = 1000000
	buffer := make([]byte, 1024)
	m := rndmap.IndexableMap[string, []byte]{}
	for i := 0; i < max; i++ {
		m.Put(strconv.Itoa(i), buffer)
	}

	showMemUsage()

	for i := 0; i < max; i++ {
		m.DeleteByKey(strconv.Itoa(i))
	}

	showMemUsage()
	runtime.GC()
	showMemUsage()
}
