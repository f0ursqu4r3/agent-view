// Package audit is the live audit. It ports the former TypeScript
// test/live-audit.ts (removed in P2c; see git history): it reads a running
// hub's /api/snapshot, checks it, and reports counts only. Neither an
// error nor the report holds a path, a name, a command or an output.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"agent-view/internal/lifecycle"
	"agent-view/internal/wire"
)

// recentMs is how long a confirmed write must be old before its agent may be
// missing from the snapshot. An agent leaves the snapshot after the 10-minute
// linger window; 9 minutes leaves a margin for clock skew.
const recentMs = 9 * 60_000

// ProviderCounts are the counts of one provider.
type ProviderCounts struct {
	Agents           int `json:"agents"`
	ConfirmedParents int `json:"confirmedParents"`
	SessionLinks     int `json:"sessionLinks"`
	ConfirmedWrites  int `json:"confirmedWrites"`
}

// Report is the audit result. Counts has a key for claude and one for codex.
type Report struct {
	Counts               map[string]ProviderCounts `json:"counts"`
	LoadedLogs           int                       `json:"loadedLogs"`
	DiscoveredLogs       int                       `json:"discoveredLogs"`
	MalformedRecords     int                       `json:"malformedRecords"`
	DegradedSources      int                       `json:"degradedSources"`
	WritesByLapsedAgents int                       `json:"writesByLapsedAgents"`
}

// JSON returns the printed form of the report: two-space indent and a final
// newline, like console.log(JSON.stringify(report, null, 2)).
func (r Report) JSON() []byte {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		// Report holds only strings and integers, so this cannot happen.
		panic(err)
	}
	return append(b, '\n')
}

// Fetch returns the body of GET /api/snapshot of the hub on 127.0.0.1:port.
// It fails unless the status is 200.
func Fetch(port int) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/snapshot", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("snapshot status")
	}
	return io.ReadAll(resp.Body)
}

// Check runs the audit checks on a snapshot body. nowMs is the current time
// in Unix milliseconds.
func Check(body []byte, nowMs int64) (Report, error) {
	var top struct {
		Envelopes json.RawMessage `json:"envelopes"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return Report{}, errors.New("snapshot body is not a JSON object")
	}
	if !bytes.HasPrefix(bytes.TrimSpace(top.Envelopes), []byte("[")) {
		return Report{}, errors.New("envelopes is an array")
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(top.Envelopes, &raws); err != nil {
		return Report{}, errors.New("envelopes is an array")
	}

	agents := map[string]wire.Agent{}
	var files []wire.FileTouch
	var r Report
	for i, raw := range raws {
		env, err := wire.DecodeEnvelope(raw)
		if err != nil {
			return Report{}, fmt.Errorf("envelope %d: %w", i, err)
		}
		switch ev := env.Ev.(type) {
		case wire.Snapshot:
			for _, a := range ev.Agents {
				agents[a.ID] = a
			}
			files = append(files, ev.Files...)
		case wire.CoverageReport:
			c := ev.Coverage
			r.LoadedLogs += c.LogsOpen
			r.DiscoveredLogs += c.LogsDiscovered
			r.MalformedRecords += c.MalformedRecords
			for _, s := range c.Sources {
				if s.State != "ok" {
					r.DegradedSources++
				}
			}
		}
	}

	// The snapshot holds only agents active inside the linger window, so a
	// provider can have none.
	if len(agents) == 0 {
		return Report{}, errors.New("at least one agent")
	}

	for _, a := range agents {
		parent := parentOf(a)
		if parent == "" {
			continue
		}
		if a.ID == parent {
			return Report{}, errors.New("agent is its own parent")
		}
		seen := map[string]bool{a.ID: true}
		p, ok := agents[parent]
		for ok {
			if seen[p.ID] {
				return Report{}, errors.New("parent cycle")
			}
			seen[p.ID] = true
			next := parentOf(p)
			if next == "" {
				break
			}
			p, ok = agents[next]
		}
	}

	// A file node lives 30 minutes, but an agent leaves the snapshot after the
	// 10-minute linger window, so an old confirmed write can name an agent that
	// is gone. A recent one must name a known agent.
	for _, f := range files {
		if f.Attribution != "confirmed" {
			continue
		}
		if f.AgentID == "" {
			return Report{}, errors.New("confirmed write has no agent ID")
		}
		if _, ok := agents[f.AgentID]; ok {
			continue
		}
		r.WritesByLapsedAgents++
		touched, ok := lifecycle.ParseISO(f.LastTouchAt)
		if !ok || nowMs-touched < recentMs {
			return Report{}, errors.New("recent confirmed write has an unknown agent")
		}
	}

	r.Counts = map[string]ProviderCounts{}
	for _, provider := range []string{"claude", "codex"} {
		var c ProviderCounts
		for _, a := range agents {
			if a.Provider != provider {
				continue
			}
			c.Agents++
			if parentOf(a) != "" {
				switch a.Link {
				case "confirmed":
					c.ConfirmedParents++
				case "session":
					c.SessionLinks++
				}
			}
		}
		for _, f := range files {
			if f.Attribution == "confirmed" && f.AgentID != "" && agents[f.AgentID].Provider == provider {
				c.ConfirmedWrites++
			}
		}
		r.Counts[provider] = c
	}
	return r, nil
}

// parentOf returns the parent ID of an agent, or "" when it has none.
func parentOf(a wire.Agent) string {
	if a.ParentID == nil {
		return ""
	}
	return *a.ParentID
}
