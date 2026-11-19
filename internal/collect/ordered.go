package collect

import "container/list"

// omap is a map that iterates in insertion order, like a JavaScript Map.
// Setting an existing key keeps its position.
type omap[V any] struct {
	idx     map[string]*list.Element
	entries list.List
}

type omapEntry[V any] struct {
	key string
	val V
}

func newOmap[V any]() *omap[V] {
	return &omap[V]{idx: map[string]*list.Element{}}
}

func (m *omap[V]) get(k string) (V, bool) {
	if e, ok := m.idx[k]; ok {
		return e.Value.(*omapEntry[V]).val, true
	}
	var zero V
	return zero, false
}

func (m *omap[V]) has(k string) bool {
	_, ok := m.idx[k]
	return ok
}

func (m *omap[V]) set(k string, v V) {
	if e, ok := m.idx[k]; ok {
		e.Value.(*omapEntry[V]).val = v
		return
	}
	m.idx[k] = m.entries.PushBack(&omapEntry[V]{key: k, val: v})
}

func (m *omap[V]) delete(k string) {
	if e, ok := m.idx[k]; ok {
		m.entries.Remove(e)
		delete(m.idx, k)
	}
}

// each calls f for every entry in insertion order. f may delete the entry it
// receives.
func (m *omap[V]) each(f func(k string, v V)) {
	for e := m.entries.Front(); e != nil; {
		next := e.Next()
		en := e.Value.(*omapEntry[V])
		f(en.key, en.val)
		e = next
	}
}

// lruSet is a set of keys, oldest first. Adding a present key moves it to the
// end. Past limit, the oldest key leaves.
type lruSet struct {
	limit int
	idx   map[string]*list.Element
	order list.List
}

func newLRUSet(limit int) *lruSet {
	return &lruSet{limit: limit, idx: map[string]*list.Element{}}
}

func (s *lruSet) has(k string) bool {
	_, ok := s.idx[k]
	return ok
}

func (s *lruSet) add(k string) {
	if e, ok := s.idx[k]; ok {
		s.order.MoveToBack(e)
		return
	}
	s.idx[k] = s.order.PushBack(k)
	if len(s.idx) > s.limit {
		oldest := s.order.Front()
		delete(s.idx, oldest.Value.(string))
		s.order.Remove(oldest)
	}
}
