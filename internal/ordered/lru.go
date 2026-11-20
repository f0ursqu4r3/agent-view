package ordered

// LRU is a least-recently-used cache of at most limit entries. Get and Set
// make an entry the newest. Has does not change the order. When a Set takes
// the length past limit, the oldest entry leaves, so a limit of 0 or less
// holds nothing. A set of keys is an LRU[K, struct{}]. An LRU is not safe for
// concurrent use.
type LRU[K comparable, V any] struct {
	limit int
	m     *Map[K, V] // oldest first
}

// NewLRU returns an empty LRU that holds at most limit entries.
func NewLRU[K comparable, V any](limit int) *LRU[K, V] {
	return &LRU[K, V]{limit: limit, m: NewMap[K, V]()}
}

// Len returns the number of entries.
func (c *LRU[K, V]) Len() int { return c.m.Len() }

// Has reports whether k is present. It does not change the order.
func (c *LRU[K, V]) Has(k K) bool { return c.m.Has(k) }

// Get returns the value of k and whether k is present. A hit makes k the
// newest entry.
func (c *LRU[K, V]) Get(k K) (V, bool) {
	v, ok := c.m.Get(k)
	if ok {
		c.m.moveToBack(k)
	}
	return v, ok
}

// Set stores v under k, replacing any value, and makes k the newest entry.
// Then the oldest entries leave while Len() is greater than the limit.
func (c *LRU[K, V]) Set(k K, v V) {
	c.m.Set(k, v)
	c.m.moveToBack(k)
	for c.m.Len() > c.limit {
		oldest, _, ok := c.m.First()
		if !ok {
			return
		}
		c.m.Delete(oldest)
	}
}
