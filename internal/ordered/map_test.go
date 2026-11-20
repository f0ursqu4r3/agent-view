package ordered

import (
	"slices"
	"testing"
)

func TestMapReplaceKeepsPosition(t *testing.T) {
	m := NewMap[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)
	m.Set("b", 20)

	var keys []string
	var vals []int
	for k, v := range m.All() {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if want := []int{1, 20, 3}; !slices.Equal(vals, want) {
		t.Fatalf("values = %v, want %v", vals, want)
	}
	if m.Len() != 3 {
		t.Fatalf("Len = %d, want 3", m.Len())
	}
}

func TestMapAllMayDeleteTheCurrentKey(t *testing.T) {
	m := NewMap[string, int]()
	for i, k := range []string{"a", "b", "c", "d"} {
		m.Set(k, i)
	}

	var seen []string
	for k := range m.All() {
		seen = append(seen, k)
		m.Delete(k)
	}
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(seen, want) {
		t.Fatalf("visited %v, want %v", seen, want)
	}
	if m.Len() != 0 {
		t.Fatalf("Len = %d, want 0", m.Len())
	}
}

func TestMapFirstOnEmpty(t *testing.T) {
	m := NewMap[string, int]()
	k, v, ok := m.First()
	if k != "" || v != 0 || ok {
		t.Fatalf("First on empty = (%q, %d, %v), want zero values and false", k, v, ok)
	}

	m.Set("only", 7)
	k, v, ok = m.First()
	if k != "only" || v != 7 || !ok {
		t.Fatalf("First = (%q, %d, %v), want (only, 7, true)", k, v, ok)
	}

	m.Delete("only")
	k, v, ok = m.First()
	if k != "" || v != 0 || ok {
		t.Fatalf("First after Delete = (%q, %d, %v), want zero values and false", k, v, ok)
	}
}

func TestMapGetHasDelete(t *testing.T) {
	m := NewMap[string, int]()
	if _, ok := m.Get("a"); ok || m.Has("a") {
		t.Fatal("empty map reports a key")
	}
	m.Set("a", 1)
	if v, ok := m.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = (%d, %v), want (1, true)", v, ok)
	}
	if !m.Has("a") {
		t.Fatal("Has(a) = false, want true")
	}
	m.Delete("missing") // no panic, no change
	if m.Len() != 1 {
		t.Fatalf("Len = %d, want 1", m.Len())
	}
	m.Delete("a")
	if m.Has("a") || m.Len() != 0 {
		t.Fatal("key a is still present after Delete")
	}
}

func TestMapMoveToBack(t *testing.T) {
	m := NewMap[string, int]()
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	if m.moveToBack("missing") {
		t.Fatal("moveToBack of an absent key = true, want false")
	}
	if !m.moveToBack("a") {
		t.Fatal("moveToBack of a present key = false, want true")
	}

	var keys []string
	for k := range m.All() {
		keys = append(keys, k)
	}
	if want := []string{"b", "c", "a"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestMapAllStopsWhenTheLoopBreaks(t *testing.T) {
	m := NewMap[int, int]()
	for i := range 5 {
		m.Set(i, i)
	}
	var n int
	for range m.All() {
		n++
		if n == 2 {
			break
		}
	}
	if n != 2 {
		t.Fatalf("visited %d entries, want 2", n)
	}
}
