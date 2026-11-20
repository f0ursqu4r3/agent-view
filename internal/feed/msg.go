// Package feed holds the Bubble Tea messages that carry the collector's
// envelopes, host states and diagnostics to the TUI.
package feed

import "agent-view/internal/schema"

// EnvelopesMsg is a batch of decoded envelopes, in arrival order.
type EnvelopesMsg []schema.Envelope

// StateMsg reports the state of a host: "connecting", "up" or "down", with
// the reason in Detail. Host is the host label as the hub reports it; the
// bridge always sets it. The model still reads an empty Host as the local
// collector (the former child-process feed); no sender uses it now.
type StateMsg struct {
	Host, State, Detail string
}

// DiagMsg is one diagnostics line, such as a line the collector wrote to
// stderr.
type DiagMsg string
