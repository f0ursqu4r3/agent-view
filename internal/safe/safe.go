// Package safe runs a callback so that a panic in it does not reach the
// caller.
package safe

// Call runs fn, when it is not nil, and recovers a panic. It returns the
// recovered value, or nil when fn did not panic. A caller that only needs to
// survive the panic ignores the result: one failing callback must not break
// the others.
func Call(fn func()) (recovered any) {
	if fn == nil {
		return nil
	}
	defer func() { recovered = recover() }()
	fn()
	return nil
}
