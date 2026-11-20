package ordered

import "testing"

func TestLRUGetMakesTheNewest(t *testing.T) {
	c := NewLRU[string, int](2)
	c.Set("a", 1)
	c.Set("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = (%d, %v), want (1, true)", v, ok)
	}
	c.Set("c", 3)

	if c.Has("b") {
		t.Fatal("b is still present, want it evicted")
	}
	if !c.Has("a") || !c.Has("c") {
		t.Fatal("a and c must remain")
	}
}

func TestLRUHasDoesNotTouch(t *testing.T) {
	c := NewLRU[string, int](2)
	c.Set("a", 1)
	c.Set("b", 2)
	if !c.Has("a") {
		t.Fatal("Has(a) = false, want true")
	}
	c.Set("c", 3)

	if c.Has("a") {
		t.Fatal("a is still present, want it evicted: Has must not change the order")
	}
	if !c.Has("b") || !c.Has("c") {
		t.Fatal("b and c must remain")
	}
}

func TestLRUSetOfPresentKeyReplacesAndMoves(t *testing.T) {
	c := NewLRU[string, int](2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("a", 2)
	c.Set("c", 3)

	if c.Has("b") {
		t.Fatal("b is still present, want it evicted")
	}
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Fatalf("Get(a) = (%d, %v), want (2, true)", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

func TestLRUSetPastLimitDropsTheOldest(t *testing.T) {
	c := NewLRU[int, int](3)
	for i := 1; i <= 5; i++ {
		c.Set(i, i*10)
	}

	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	for _, k := range []int{1, 2} {
		if c.Has(k) {
			t.Fatalf("key %d is still present, want it evicted", k)
		}
	}
	for _, k := range []int{3, 4, 5} {
		if v, ok := c.Get(k); !ok || v != k*10 {
			t.Fatalf("Get(%d) = (%d, %v), want (%d, true)", k, v, ok, k*10)
		}
	}
}

func TestLRULimitZeroHoldsNothing(t *testing.T) {
	c := NewLRU[string, int](0)
	c.Set("a", 1)
	if c.Len() != 0 {
		t.Fatalf("Len = %d, want 0", c.Len())
	}
	if c.Has("a") {
		t.Fatal("a is present in a cache with limit 0")
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("Get(a) hit in a cache with limit 0")
	}
}

func TestLRUNegativeLimitHoldsNothing(t *testing.T) {
	c := NewLRU[string, int](-1)
	c.Set("a", 1)
	if c.Len() != 0 {
		t.Fatalf("Len = %d, want 0", c.Len())
	}
}

func TestLRUGetMiss(t *testing.T) {
	c := NewLRU[string, int](2)
	v, ok := c.Get("a")
	if ok || v != 0 {
		t.Fatalf("Get on empty = (%d, %v), want (0, false)", v, ok)
	}
}

func TestLRUAsSet(t *testing.T) {
	s := NewLRU[string, struct{}](2)
	s.Set("a", struct{}{})
	s.Set("b", struct{}{})
	if !s.Has("a") || !s.Has("b") {
		t.Fatal("a and b must be present")
	}
	s.Set("c", struct{}{})
	if s.Has("a") {
		t.Fatal("a is still present, want it evicted")
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
}
