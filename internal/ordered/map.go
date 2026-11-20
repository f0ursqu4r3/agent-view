// Package ordered holds one generic ordered map and one generic LRU cache.
package ordered

import (
	"container/list"
	"iter"
)

// Map is a map that iterates in insertion order, like a JavaScript Map.
// Setting an existing key replaces its value and keeps its position.
// A Map is not safe for concurrent use.
type Map[K comparable, V any] struct {
	idx     map[K]*list.Element
	entries list.List // oldest first
}

type entry[K comparable, V any] struct {
	key K
	val V
}

// NewMap returns an empty Map.
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{idx: map[K]*list.Element{}}
}

// Len returns the number of keys.
func (m *Map[K, V]) Len() int { return len(m.idx) }

// Has reports whether k is present.
func (m *Map[K, V]) Has(k K) bool {
	_, ok := m.idx[k]
	return ok
}

// Get returns the value of k and whether k is present.
func (m *Map[K, V]) Get(k K) (V, bool) {
	if e, ok := m.idx[k]; ok {
		return e.Value.(*entry[K, V]).val, true
	}
	var zero V
	return zero, false
}

// Set stores v under k. A new key goes to the end. An existing key keeps its
// position and takes the new value.
func (m *Map[K, V]) Set(k K, v V) {
	if e, ok := m.idx[k]; ok {
		e.Value.(*entry[K, V]).val = v
		return
	}
	m.idx[k] = m.entries.PushBack(&entry[K, V]{key: k, val: v})
}

// Delete removes k. It does nothing when k is absent.
func (m *Map[K, V]) Delete(k K) {
	if e, ok := m.idx[k]; ok {
		m.entries.Remove(e)
		delete(m.idx, k)
	}
}

// First returns the oldest entry. It reports false when the map is empty.
func (m *Map[K, V]) First() (K, V, bool) {
	if e := m.entries.Front(); e != nil {
		en := e.Value.(*entry[K, V])
		return en.key, en.val, true
	}
	var k K
	var v V
	return k, v, false
}

// All yields the entries in insertion order, oldest first. The loop body may
// delete the current key only: the next entry is read before the yield, so
// deleting the next key ends the loop early.
func (m *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for e := m.entries.Front(); e != nil; {
			next := e.Next()
			en := e.Value.(*entry[K, V])
			if !yield(en.key, en.val) {
				return
			}
			e = next
		}
	}
}

// moveToBack makes k the newest entry. It reports whether k is present.
func (m *Map[K, V]) moveToBack(k K) bool {
	e, ok := m.idx[k]
	if ok {
		m.entries.MoveToBack(e)
	}
	return ok
}
