package store

import (
	"container/list"
	"iter"
)

// omap is a map that iterates in insertion order, like a JavaScript Map.
// Setting an existing key keeps its position.
type omap[K comparable, V any] struct {
	idx     map[K]*list.Element
	entries list.List
}

type omapEntry[K comparable, V any] struct {
	key K
	val V
}

func newOmap[K comparable, V any]() *omap[K, V] {
	return &omap[K, V]{idx: map[K]*list.Element{}}
}

func (m *omap[K, V]) len() int { return len(m.idx) }

func (m *omap[K, V]) has(k K) bool {
	_, ok := m.idx[k]
	return ok
}

func (m *omap[K, V]) get(k K) (V, bool) {
	if e, ok := m.idx[k]; ok {
		return e.Value.(*omapEntry[K, V]).val, true
	}
	var zero V
	return zero, false
}

func (m *omap[K, V]) set(k K, v V) {
	if e, ok := m.idx[k]; ok {
		e.Value.(*omapEntry[K, V]).val = v
		return
	}
	m.idx[k] = m.entries.PushBack(&omapEntry[K, V]{key: k, val: v})
}

func (m *omap[K, V]) delete(k K) {
	if e, ok := m.idx[k]; ok {
		m.entries.Remove(e)
		delete(m.idx, k)
	}
}

func (m *omap[K, V]) first() (K, V, bool) {
	if e := m.entries.Front(); e != nil {
		en := e.Value.(*omapEntry[K, V])
		return en.key, en.val, true
	}
	var k K
	var v V
	return k, v, false
}

// all yields the entries in insertion order. The loop body may delete the current key only.
func (m *omap[K, V]) all() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for e := m.entries.Front(); e != nil; {
			next := e.Next()
			en := e.Value.(*omapEntry[K, V])
			if !yield(en.key, en.val) {
				return
			}
			e = next
		}
	}
}
