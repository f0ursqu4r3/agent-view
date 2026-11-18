package store

// ChangeKind names one effect of an applied envelope.
type ChangeKind uint8

// The kinds of change.
const (
	CallStarted ChangeKind = iota
	CallEnded
	AgentAdded
	AgentStatus
	CallOutput
)

// Change is one effect of an applied envelope.
type Change struct {
	Kind                          ChangeKind
	Host, AgentID, CallID, Status string
	AtMs                          int64
	History                       bool
}
