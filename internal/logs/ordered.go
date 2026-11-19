package logs

import "container/list"

// orderedMap is a string map that keeps insertion order, like a JavaScript
// Map: setting an existing key replaces its value and keeps its position.
type orderedMap[V any] struct {
	idx     map[string]*list.Element
	entries list.List
}

type orderedEntry[V any] struct {
	key string
	val V
}

func newOrderedMap[V any]() *orderedMap[V] {
	return &orderedMap[V]{idx: map[string]*list.Element{}}
}

func (m *orderedMap[V]) get(key string) (V, bool) {
	if e, ok := m.idx[key]; ok {
		return e.Value.(*orderedEntry[V]).val, true
	}
	var zero V
	return zero, false
}

func (m *orderedMap[V]) has(key string) bool {
	_, ok := m.idx[key]
	return ok
}

func (m *orderedMap[V]) set(key string, val V) {
	if e, ok := m.idx[key]; ok {
		e.Value.(*orderedEntry[V]).val = val
		return
	}
	m.idx[key] = m.entries.PushBack(&orderedEntry[V]{key, val})
}

func (m *orderedMap[V]) delete(key string) {
	if e, ok := m.idx[key]; ok {
		m.entries.Remove(e)
		delete(m.idx, key)
	}
}

func (m *orderedMap[V]) len() int { return len(m.idx) }

func (m *orderedMap[V]) deleteOldest() {
	if e := m.entries.Front(); e != nil {
		m.delete(e.Value.(*orderedEntry[V]).key)
	}
}

// boundedSet is a set that forgets its oldest keys beyond max. Adding a key
// again moves it to the end.
type boundedSet struct {
	keys *orderedMap[struct{}]
	max  int
}

func newBoundedSet(max int) *boundedSet {
	return &boundedSet{keys: newOrderedMap[struct{}](), max: max}
}

func (s *boundedSet) has(key string) bool { return s.keys.has(key) }

func (s *boundedSet) add(key string) {
	s.keys.delete(key)
	s.keys.set(key, struct{}{})
	if s.keys.len() > s.max {
		s.keys.deleteOldest()
	}
}
