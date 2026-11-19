// Package schema mirrors the event schema in the former TypeScript
// shared/schema.ts (removed in P2c; see git history).
package schema

import "encoding/json"

// Version is the only envelope version this program accepts.
const Version = 1

// Agent is one main agent or sub-agent.
type Agent struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	ParentID    *string `json:"parentId"`
	Link        string  `json:"link"`
	Cwd         *string `json:"cwd"`
	ProjectRoot *string `json:"projectRoot"`
	Model       string  `json:"model"`
	Status      string  `json:"status"`
	LastEventAt string  `json:"lastEventAt"`
}

// Call is one tool call.
type Call struct {
	AgentID   string   `json:"agentId"`
	CallID    string   `json:"callId"`
	StartedAt string   `json:"startedAt"`
	Category  string   `json:"category"`
	RawName   string   `json:"rawName"`
	Files     []string `json:"files"`
	Command   string   `json:"command"`
	Pattern   string   `json:"pattern"`
}

// FileTouch is one file entry in a snapshot.
type FileTouch struct {
	Path        string `json:"path"`
	LastTouchAt string `json:"lastTouchAt"`
	Attribution string `json:"attribution"`
	AgentID     string `json:"agentId"`
}

// Write is one file write reported by tool.end.
type Write struct {
	Path string `json:"path"`
	Op   string `json:"op"`
}

// SourceState is the health of one event source.
type SourceState struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Coverage describes what the collector watches.
type Coverage struct {
	LogRoots         []string               `json:"logRoots"`
	LogsOpen         int                    `json:"logsOpen"`
	LogsDiscovered   int                    `json:"logsDiscovered"`
	WatchRoots       []string               `json:"watchRoots"`
	MalformedRecords int                    `json:"malformedRecords"`
	FSDropped        int                    `json:"fsDropped"`
	Sources          map[string]SourceState `json:"sources"`
}

// Envelope is one NDJSON line. It holds the fields of every event kind;
// only the fields of Kind are set.
//
// The "files" field has two shapes: a string list for tool.update and a
// FileTouch list for snapshot. RawFiles keeps it undecoded; use UpdateFiles
// or SnapshotFiles.
type Envelope struct {
	V    int    `json:"v"`
	Host string `json:"host"`
	Seq  int64  `json:"seq"`
	Ts   string `json:"ts"`
	Kind string `json:"kind"`

	Agent            *Agent          `json:"agent"`
	AgentID          string          `json:"agentId"`
	Status           string          `json:"status"`
	Call             *Call           `json:"call"`
	CallID           string          `json:"callId"`
	Chunk            string          `json:"chunk"`
	Truncated        bool            `json:"truncated"`
	Ok               *bool           `json:"ok"`
	ExitCode         *int            `json:"exitCode"`
	Writes           []Write         `json:"writes"`
	Path             string          `json:"path"`
	Op               string          `json:"op"`
	Agents           []Agent         `json:"agents"`
	OpenCalls        []Call          `json:"openCalls"`
	Coverage         *Coverage       `json:"coverage"`
	Source           string          `json:"source"`
	State            string          `json:"state"`
	Detail           string          `json:"detail"`
	Now              string          `json:"now"`
	CollectorVersion string          `json:"collectorVersion"`
	Platform         string          `json:"platform"`
	Demo             bool            `json:"demo"`
	RawFiles         json.RawMessage `json:"files"`
}

// UpdateFiles returns the files of a tool.update event. It returns nil for
// any other kind or when "files" is not a string list.
func (e Envelope) UpdateFiles() []string {
	if e.Kind != "tool.update" || len(e.RawFiles) == 0 {
		return nil
	}
	var files []string
	if err := json.Unmarshal(e.RawFiles, &files); err != nil {
		return nil
	}
	return files
}

// SnapshotFiles returns the files of a snapshot event. It returns nil for
// any other kind or when "files" is not a FileTouch list.
func (e Envelope) SnapshotFiles() []FileTouch {
	if e.Kind != "snapshot" || len(e.RawFiles) == 0 {
		return nil
	}
	var files []FileTouch
	if err := json.Unmarshal(e.RawFiles, &files); err != nil {
		return nil
	}
	return files
}

// Label returns the call-card label for a category. Unknown categories give TOOL.
func Label(category string) string {
	switch category {
	case "read":
		return "READ"
	case "edit":
		return "EDIT"
	case "write":
		return "WRITE"
	case "command":
		return "RUN"
	case "search":
		return "GREP"
	case "agent":
		return "SPAWN"
	default:
		return "TOOL"
	}
}
