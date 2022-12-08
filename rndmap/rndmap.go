package rndmap

import (
	"fmt"
	"sync"
)

const blockSize = 32

// IndexableMap is a container that efficiently supports map and index
// operations. The implementation is GC-friendly, allowing for memory to
// be released, once items are removed from it.
type IndexableMap[TK comparable, TV any] struct {
	mu     sync.RWMutex
	values [][blockSize]kv[TK, TV] // using slice of arrays to allow releasing memory efficiently
	keys   map[TK]int              // map points to the index on "values"
}

type kv[TK comparable, TV any] struct {
	key   TK
	value TV
}

func (r *IndexableMap[TK, TV]) Size() int {
	return len(r.keys)
}

func (r *IndexableMap[TK, TV]) GetByKey(key TK) (TV, bool) {
	var zero TV

	ix, ok := r.keys[key]
	if !ok {
		return zero, false
	}

	_, value := r.GetByIndex(ix)
	return value, true
}

func (r *IndexableMap[TK, TV]) GetByIndex(index int) (TK, TV) {
	r.indexMustBeValid(index)
	blockix := index / blockSize
	blockos := index % blockSize
	ret := r.values[blockix][blockos]
	return ret.key, ret.value
}

func (r *IndexableMap[TK, TV]) Put(key TK, value TV) {
	newkv := kv[TK, TV]{key: key, value: value}
	ix, ok := r.keys[key]
	if ok {
		// replace in-place
		blockix := ix / blockSize
		blockos := ix % blockSize
		r.values[blockix][blockos] = newkv
		return
	}

	// replace in-place
	if r.keys == nil {
		r.keys = map[TK]int{}
	}

	// add new
	ix = len(r.keys)
	r.keys[key] = ix

	blockix := ix / blockSize
	blockos := ix % blockSize

	// needs a brand new block
	if blockos == 0 {
		r.values = append(r.values, [blockSize]kv[TK, TV]{newkv})
	} else {
		r.values[blockix][blockos] = newkv
	}
}

func (r *IndexableMap[TK, TV]) DeleteByKey(key TK) {
	ix, ok := r.keys[key]
	if !ok {
		return
	}

	r.DeleteByIndex(ix)
}

func (r *IndexableMap[TK, TV]) DeleteByIndex(index int) {
	r.indexMustBeValid(index)

	blockix := index / blockSize
	blockos := index % blockSize

	// delete from the key map
	key := r.values[blockix][blockos].key
	delete(r.keys, key)

	// delete from the value slice/array
	lastIx := len(r.keys)
	blockixLast := lastIx / blockSize
	blockosLast := lastIx % blockSize

	// move last to the vacant position and update references
	r.values[blockix][blockos] = r.values[blockixLast][blockosLast]

	if lastIx != index {
		r.keys[r.values[blockix][blockos].key] = index
	}

	// if the last block is unused, release it
	if blockosLast == 0 {
		r.values = r.values[:blockixLast]
	}
}

func (r *IndexableMap[TK, TV]) Items() func() (TK, TV, bool) {
	index := 0
	return func() (TK, TV, bool) {
		if index >= r.Size() {
			return *new(TK), *new(TV), false
		}

		key, value := r.GetByIndex(index)
		index++
		return key, value, true
	}
}

func (r *IndexableMap[TK, TV]) indexMustBeValid(index int) {
	if index < 0 || index >= len(r.keys) {
		panic(fmt.Sprintf(
			"index %d is outside the valid range [0;%d)",
			index, len(r.keys)))
	}
}
