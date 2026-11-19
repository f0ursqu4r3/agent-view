// Package wire is the collector's NDJSON schema: it encodes and decodes the
// envelopes. It mirrors src/shared/schema.ts. Package schema is the TUI's own
// decoder of the same lines.
package wire

// SchemaVersion is the envelope version the collector writes.
const SchemaVersion = 1

// CollectorVersion is the collector version (the package.json version).
const CollectorVersion = "0.2.0"

// Agent is one main agent or sub-agent. ParentID, Cwd and ProjectRoot encode
// null when nil. Model is omitted when empty.
type Agent struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	ParentID    *string `json:"parentId"`
	Link        string  `json:"link"`
	Cwd         *string `json:"cwd"`
	ProjectRoot *string `json:"projectRoot"`
	Model       string  `json:"model,omitempty"`
	Status      string  `json:"status"`
	LastEventAt string  `json:"lastEventAt"`
}

// Call is one tool call. Files, Command and Pattern are omitted when empty.
type Call struct {
	AgentID   string   `json:"agentId"`
	CallID    string   `json:"callId"`
	StartedAt string   `json:"startedAt"`
	Category  string   `json:"category"`
	RawName   string   `json:"rawName"`
	Files     []string `json:"files,omitempty"`
	Command   string   `json:"command,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
}

// FileTouch is one recently touched file. AgentID is omitted when empty.
type FileTouch struct {
	Path        string `json:"path"`
	LastTouchAt string `json:"lastTouchAt"`
	Attribution string `json:"attribution"`
	AgentID     string `json:"agentId,omitempty"`
}

// Write is one file write that a finished call made.
type Write struct {
	Path string `json:"path"`
	Op   string `json:"op"`
}

// SourceState is the health of one source. Detail is omitted when empty.
type SourceState struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Coverage describes what the collector watches. Nil slices encode as []
// and a nil map encodes as {}.
type Coverage struct {
	LogRoots         []string               `json:"logRoots"`
	LogsOpen         int                    `json:"logsOpen"`
	LogsDiscovered   int                    `json:"logsDiscovered"`
	WatchRoots       []string               `json:"watchRoots"`
	MalformedRecords int                    `json:"malformedRecords"`
	FSDropped        int                    `json:"fsDropped"`
	Sources          map[string]SourceState `json:"sources"`
}

// MarshalJSON encodes nil slices as [] and a nil map as {}.
func (c Coverage) MarshalJSON() ([]byte, error) {
	type plain Coverage
	p := plain(c)
	if p.LogRoots == nil {
		p.LogRoots = []string{}
	}
	if p.WatchRoots == nil {
		p.WatchRoots = []string{}
	}
	if p.Sources == nil {
		p.Sources = map[string]SourceState{}
	}
	return marshalValue(p)
}

// Event is one collector event. Kind is the wire value of the "kind" key.
type Event interface{ Kind() string }

// Hello opens a stream. Demo encodes only when true.
type Hello struct {
	CollectorVersion string `json:"collectorVersion"`
	Platform         string `json:"platform"`
	Now              string `json:"now"`
	Demo             bool   `json:"demo,omitempty"`
}

// Snapshot is the full state. Nil lists encode as [].
type Snapshot struct {
	Agents    []Agent     `json:"agents"`
	OpenCalls []Call      `json:"openCalls"`
	Files     []FileTouch `json:"files"`
}

// MarshalJSON encodes nil lists as [].
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type plain Snapshot
	p := plain(s)
	if p.Agents == nil {
		p.Agents = []Agent{}
	}
	if p.OpenCalls == nil {
		p.OpenCalls = []Call{}
	}
	if p.Files == nil {
		p.Files = []FileTouch{}
	}
	return marshalValue(p)
}

// AgentUpsert adds or replaces an agent.
type AgentUpsert struct {
	Agent Agent `json:"agent"`
}

// AgentStatusChange sets an agent's status. Its kind is "agent.status".
type AgentStatusChange struct {
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
}

// ToolStart opens a call.
type ToolStart struct {
	Call Call `json:"call"`
}

// ToolUpdate sets the files of an open call. A nil list encodes as [].
type ToolUpdate struct {
	AgentID string   `json:"agentId"`
	CallID  string   `json:"callId"`
	Files   []string `json:"files"`
}

// MarshalJSON encodes a nil Files as [].
func (u ToolUpdate) MarshalJSON() ([]byte, error) {
	type plain ToolUpdate
	p := plain(u)
	if p.Files == nil {
		p.Files = []string{}
	}
	return marshalValue(p)
}

// ToolOutput is one chunk of call output. Truncated always encodes.
type ToolOutput struct {
	AgentID   string `json:"agentId"`
	CallID    string `json:"callId"`
	Chunk     string `json:"chunk"`
	Truncated bool   `json:"truncated"`
}

// ToolEnd closes a call. Ok encodes null when nil. ExitCode and Writes are
// omitted when nil; an empty non-nil Writes encodes as [].
type ToolEnd struct {
	AgentID  string  `json:"agentId"`
	CallID   string  `json:"callId"`
	Ok       *bool   `json:"ok"`
	ExitCode *int    `json:"exitCode,omitempty"`
	Writes   []Write `json:"writes,omitzero"`
}

// FsChange is one file-system change.
type FsChange struct {
	Path string `json:"path"`
	Op   string `json:"op"`
}

// SourceStatus reports the health of one source. The state fields are inline.
type SourceStatus struct {
	Source string `json:"source"`
	SourceState
}

// CoverageReport carries the coverage. Its kind is "coverage".
type CoverageReport struct {
	Coverage Coverage `json:"coverage"`
}

func (Hello) Kind() string             { return "hello" }
func (Snapshot) Kind() string          { return "snapshot" }
func (AgentUpsert) Kind() string       { return "agent.upsert" }
func (AgentStatusChange) Kind() string { return "agent.status" }
func (ToolStart) Kind() string         { return "tool.start" }
func (ToolUpdate) Kind() string        { return "tool.update" }
func (ToolOutput) Kind() string        { return "tool.output" }
func (ToolEnd) Kind() string           { return "tool.end" }
func (FsChange) Kind() string          { return "fs.change" }
func (SourceStatus) Kind() string      { return "source.status" }
func (CoverageReport) Kind() string    { return "coverage" }

// Raw is an event of a kind this version does not know. Fields is the event's
// JSON object without the envelope keys, with the members as received, so the
// hub forwards it unchanged. A nil Fields encodes as an empty object.
type Raw struct {
	KindName string
	Fields   []byte
}

// Kind returns the kind that the line named.
func (r Raw) Kind() string { return r.KindName }

// MarshalJSON returns Fields, or an empty object when Fields is empty.
func (r Raw) MarshalJSON() ([]byte, error) {
	if len(r.Fields) == 0 {
		return []byte("{}"), nil
	}
	return r.Fields, nil
}

// HostStatus is the state of one host's source. Detail is omitted when empty.
type HostStatus struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// SourceEvent is an event with its time, before the envelope adds the host
// and the sequence number.
type SourceEvent struct {
	Ts string
	Ev Event
}

// Envelope is one NDJSON line.
type Envelope struct {
	V    int
	Host string
	Seq  int64
	Ts   string
	Ev   Event
}
