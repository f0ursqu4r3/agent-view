// Package feed runs the collector and turns its output into Bubble Tea
// messages for the TUI.
package feed

import "agent-view/internal/schema"

// EnvelopesMsg is a batch of decoded envelopes, in arrival order.
type EnvelopesMsg []schema.Envelope

// StateMsg reports the state of the collector of a host: "up" or "down",
// with the reason in Detail. An empty Host means the local collector, whose
// host label is not known yet.
type StateMsg struct {
	Host, State, Detail string
}

// DiagMsg is one diagnostics line, such as a line the collector wrote to
// stderr.
type DiagMsg string
