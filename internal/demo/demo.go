package demo

import (
	"fmt"
	"math"
	"path"
	"sync"

	"agent-view/internal/wire"
)

const (
	stressFiles           = 3000
	stressAgentSessions   = 10
	stressSubsPerSession  = 4
	stressStartsPerSecond = 78
	stressTickMs          = 50
	callsPerTurn          = 6 // after this many calls an agent goes idle for a while
	fileStride            = 7
	demoSeed              = 20261117
	maxStepGapMs          = 250
)

const (
	modelClaude = "claude-sonnet-5-5"
	modelCodex  = "gpt-5-codex"
)

type project struct {
	root  string
	files []string
}

type demoAgent struct {
	agent     wire.Agent
	project   *project
	provider  string
	busy      bool
	restUntil float64
	calls     int
	status    string
}

type command struct {
	text     string
	lines    []string
	failLine string
}

func prefixed(root string, files ...string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = root + "/" + f
	}
	return out
}

func normalProjects() []*project {
	return []*project{
		{root: "/demo/agent-view", files: prefixed("/demo/agent-view",
			"src/viewer/drawer.ts", "src/viewer/stage.ts", "src/viewer/tree.ts", "src/viewer/colors.ts",
			"src/shared/schema.ts", "src/collector/state.ts", "test/viewer/drawer.test.ts", "public/index.html")},
		{root: "/demo/search-service", files: prefixed("/demo/search-service",
			"src/index.ts", "src/query/parser.ts", "src/query/ranking.ts", "src/store/segments.ts",
			"src/store/merge.ts", "src/api/routes.ts", "test/ranking.test.ts", "README.md")},
	}
}

var stressProjectNames = []string{"agent-view", "search-service", "billing-api", "web-client", "infra-tools"}

func stressProjects() []*project {
	per := stressFiles / len(stressProjectNames)
	out := make([]*project, len(stressProjectNames))
	for n, name := range stressProjectNames {
		root := "/demo/" + name
		files := make([]string, per)
		for i := range files {
			files[i] = fmt.Sprintf("%s/src/area%02d/module%03d.ts", root, i%30, i)
		}
		out[n] = &project{root: root, files: files}
	}
	return out
}

func projectsFor(scale string) []*project {
	if scale == "stress" {
		return stressProjects()
	}
	return normalProjects()
}

// FilePaths lists every file path the demo can touch. Stress has exactly 3,000.
func FilePaths(scale string) []string {
	var out []string
	for _, p := range projectsFor(scale) {
		out = append(out, p.files...)
	}
	return out
}

var commands = []command{
	{"bun test test/viewer", []string{"bun test v1.4.4", "  drawer renders open calls (12ms)", "  drawer keeps 16 seconds of history (9ms)", " 14 pass", " 0 fail"}, " 1 fail"},
	{"bun run typecheck", []string{"$ tsc --noEmit", "Found 0 errors."}, "src/viewer/drawer.ts(41,9): error TS2322"},
	{"git status --short", []string{" M src/viewer/drawer.ts", "?? test/viewer/drawer.test.ts"}, "fatal: not a git repository"},
	{"bun run build", []string{"bundled 128 modules in 84ms", "dist/index.js  412 KB"}, "error: could not resolve \"./missing\""},
}

var patterns = []string{"renderDrawer", "lingerMs", "SourceEvent", "rankResults", "mergeSegments", "TODO"}

// Options configure a Source. Emit is called with the demo's lock held, so it
// must not call Stop. Sched defaults to the real scheduler.
type Options struct {
	Host  string
	Scale string // "normal" (default) or "stress"
	Emit  func([]wire.SourceEvent)
	Sched Scheduler
}

// Source is the simulated agents. Paths are fake and live under /demo/...
// Every event carries ts. Normal scale: one Claude Code session and one Codex
// session, each with two sub-agents. Stress scale: 50 agents across five
// projects, 3,000 files, about 200 events per second.
//
// Every callback runs under mu. On the real scheduler this makes Stop final:
// nothing is emitted after Stop returns.
type Source struct {
	opts  Options
	scale string
	sched Scheduler
	rand  func() float64

	mu        sync.Mutex
	agents    []*demoAgent
	timers    map[int]func()
	timerID   int
	running   bool
	cursors   map[*project]int
	nextAgent int
	callCount int
	lastStep  float64
	budget    float64
}

// New returns a stopped Source.
func New(o Options) *Source {
	s := &Source{opts: o, scale: "normal", sched: o.Sched, rand: Mulberry32(demoSeed),
		timers: map[int]func(){}, cursors: map[*project]int{}}
	if o.Scale == "stress" {
		s.scale = "stress"
	}
	if s.sched == nil {
		s.sched = NewRealScheduler()
	}
	return s
}

// Start announces the agents and begins the simulation. A second call does nothing.
func (s *Source) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.agents = s.buildAgents()
	now := s.sched.NowMs()
	s.lastStep = now
	ts := wire.FormatISO(int64(now))
	events := make([]wire.SourceEvent, len(s.agents))
	for i, a := range s.agents {
		ag := a.agent
		ag.LastEventAt = ts
		events[i] = wire.SourceEvent{Ts: ts, Ev: wire.AgentUpsert{Agent: ag}}
	}
	s.send(events)
	s.step()
}

// Stop cancels every pending timer. It is safe to call more than once.
func (s *Source) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	for id, cancel := range s.timers {
		cancel()
		delete(s.timers, id)
	}
}

func (s *Source) between(lo, hi float64) float64 {
	return lo + float64(s.rand()*(hi-lo))
}

func (s *Source) later(ms float64, fn func()) {
	id := s.timerID
	s.timerID++
	s.timers[id] = s.sched.After(ms, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.timers, id)
		if s.running {
			fn()
		}
	})
}

func (s *Source) send(events []wire.SourceEvent) {
	if !s.running || len(events) == 0 {
		return
	}
	defer func() { _ = recover() }() // a failing consumer must not stop the demo
	s.opts.Emit(events)
}

func strp(v string) *string { return &v }

func (s *Source) buildAgents() []*demoAgent {
	host := s.opts.Host
	projects := projectsFor(s.scale)
	now := wire.FormatISO(int64(s.sched.NowMs()))
	mk := func(id, provider, name string, parentID *string, p *project, n int) *demoAgent {
		role, link := "main", "root"
		if parentID != nil {
			role, link = "sub-agent", "confirmed"
		}
		model := modelClaude
		if provider != "claude" {
			model = modelCodex
		}
		return &demoAgent{
			agent: wire.Agent{
				ID: id, Provider: provider, Name: name, Role: role, ParentID: parentID, Link: link,
				Cwd: strp(p.root), ProjectRoot: strp(p.root), Model: model, Status: "working", LastEventAt: now,
			},
			project: p, provider: provider, calls: n % callsPerTurn, status: "working",
		}
	}
	var out []*demoAgent
	if s.scale == "normal" {
		claude := host + "/claude:demo:session-1"
		codex := host + "/codex:demo:thread-1"
		return append(out,
			mk(claude, "claude", "Refactor drawer layout", nil, projects[0], 0),
			mk(claude+":review", "claude", "Review drawer styles", strp(claude), projects[0], 1),
			mk(claude+":tests", "claude", "Update drawer tests", strp(claude), projects[0], 2),
			mk(codex, "codex", "Build workspace search", nil, projects[1], 3),
			mk(codex+":index", "codex", "Index source files", strp(codex), projects[1], 4),
			mk(codex+":ranking", "codex", "Check ranking results", strp(codex), projects[1], 5),
		)
	}
	n := 0
	for sess := 0; sess < stressAgentSessions; sess++ {
		provider, kind := "claude", "session"
		if sess%2 != 0 {
			provider, kind = "codex", "thread"
		}
		p := projects[sess%len(projects)]
		id := fmt.Sprintf("%s/%s:demo:%s-%d", host, provider, kind, sess+1)
		out = append(out, mk(id, provider, fmt.Sprintf("%s lead %d", path.Base(p.root), sess+1), nil, p, n))
		n++
		for k := 0; k < stressSubsPerSession; k++ {
			out = append(out, mk(fmt.Sprintf("%s:w%d", id, k+1), provider,
				fmt.Sprintf("Worker %d.%d", sess+1, k+1), strp(id), p, n))
			n++
		}
	}
	return out
}

// step starts calls on idle agents, then schedules the next step.
func (s *Source) step() {
	if !s.running {
		return
	}
	now := s.sched.NowMs()
	starts := 1
	if s.scale == "stress" {
		s.budget += float64(stressStartsPerSecond * float64(math.Min(now-s.lastStep, maxStepGapMs)/1000))
		starts = int(math.Floor(s.budget))
		s.budget -= float64(starts)
	}
	s.lastStep = now
	var events []wire.SourceEvent
	for i := 0; i < starts; i++ {
		a := s.pickIdle(now)
		if a == nil {
			break
		}
		events = s.startCall(a, now, events)
	}
	s.send(events)
	delay := float64(stressTickMs)
	if s.scale != "stress" {
		delay = s.between(160, 480)
	}
	s.later(delay, s.step)
}

func (s *Source) pickIdle(now float64) *demoAgent {
	for i := range s.agents {
		a := s.agents[(s.nextAgent+i)%len(s.agents)]
		if a.busy || a.restUntil > now {
			continue
		}
		s.nextAgent = (s.nextAgent + i + 1) % len(s.agents)
		return a
	}
	return nil
}

func (s *Source) pickFile(a *demoAgent) string {
	// One cursor per project: consecutive picks walk the whole pool before any file repeats.
	cursor := s.cursors[a.project] + fileStride
	s.cursors[a.project] = cursor
	return a.project.files[cursor%len(a.project.files)]
}

func (s *Source) startCall(a *demoAgent, now float64, out []wire.SourceEvent) []wire.SourceEvent {
	ts := wire.FormatISO(int64(now))
	r := s.rand()
	var kind string
	switch {
	case r < 0.35:
		kind = "read"
	case r < 0.6:
		kind = "edit"
	case r < 0.8:
		kind = "command"
	default:
		kind = "search"
	}
	speed := 1.0
	if s.scale == "stress" {
		speed = 0.25
	}
	var span float64
	switch kind {
	case "read":
		span = s.between(350, 900)
	case "edit":
		span = s.between(700, 1600)
	case "command":
		span = s.between(1600, 4200)
	default:
		span = s.between(450, 1000)
	}
	duration := speed * span
	s.callCount++
	callID := fmt.Sprintf("d%d", s.callCount)
	claude := a.provider == "claude"
	file := ""
	if kind == "read" || kind == "edit" {
		file = s.pickFile(a)
	}
	cmd := commands[int(math.Floor(s.rand()*float64(len(commands))))]
	pattern := patterns[int(math.Floor(s.rand()*float64(len(patterns))))]

	// Codex has no read or search tools: it reads and searches through shell commands.
	var rawName, commandText string
	var output []string
	fail := false
	switch kind {
	case "edit":
		rawName = pick(claude, "Edit", "apply_patch")
	case "read":
		rawName = pick(claude, "Read", "shell")
		if !claude {
			commandText = fmt.Sprintf("sed -n '1,120p' %s", file)
		}
	case "search":
		rawName = pick(claude, "Grep", "shell")
		if !claude {
			commandText = fmt.Sprintf("rg -n \"%s\" src", pattern)
		}
	default:
		rawName = pick(claude, "Bash", "shell")
		commandText = cmd.text
		fail = s.rand() < 0.12
		if fail {
			output = append(append([]string{}, cmd.lines[:2]...), cmd.failLine)
		} else {
			output = cmd.lines
		}
	}
	call := wire.Call{AgentID: a.agent.ID, CallID: callID, StartedAt: ts,
		Category: wire.CategoryFor(a.provider, rawName), RawName: rawName}
	if file != "" && (claude || kind == "edit") {
		call.Files = []string{file}
	}
	call.Command = commandText
	if claude && kind == "search" {
		call.Pattern = pattern
	}

	a.busy = true
	if a.status != "working" {
		a.status = "working"
		out = append(out, wire.SourceEvent{Ts: ts, Ev: wire.AgentStatusChange{AgentID: a.agent.ID, Status: "working"}})
	}
	out = append(out, wire.SourceEvent{Ts: ts, Ev: wire.ToolStart{Call: call}})
	if s.callCount%8 == 0 {
		p := a.project.files[int(math.Floor(s.rand()*float64(len(a.project.files))))]
		out = append(out, wire.SourceEvent{Ts: ts, Ev: wire.FsChange{Path: p, Op: "change"}})
	}

	// Codex streams output while a command runs. Claude Code writes it with the result.
	chunk := ""
	for _, l := range output {
		chunk += l + "\n"
	}
	streamNow := !claude && len(output) > 0
	if streamNow {
		s.later(duration*0.55, func() {
			s.send([]wire.SourceEvent{{Ts: wire.FormatISO(int64(s.sched.NowMs())), Ev: wire.ToolOutput{
				AgentID: a.agent.ID, CallID: callID, Chunk: chunk, Truncated: false}}})
		})
	}
	s.later(duration, func() {
		end := wire.FormatISO(int64(s.sched.NowMs()))
		var events []wire.SourceEvent
		if !streamNow && len(output) > 0 {
			events = append(events, wire.SourceEvent{Ts: end, Ev: wire.ToolOutput{
				AgentID: a.agent.ID, CallID: callID, Chunk: chunk, Truncated: false}})
		}
		ok := !fail
		e := wire.ToolEnd{AgentID: a.agent.ID, CallID: callID, Ok: &ok}
		if kind == "command" {
			code := 0
			if fail {
				code = 1
			}
			e.ExitCode = &code
		}
		if kind == "edit" && file != "" {
			e.Writes = []wire.Write{{Path: file, Op: "edit"}}
		}
		events = append(events, wire.SourceEvent{Ts: end, Ev: e})
		a.busy = false
		a.calls++
		if a.calls%callsPerTurn == 0 {
			a.status = "idle"
			rest := 2500.0
			if s.scale == "stress" {
				rest = 200
			}
			a.restUntil = s.sched.NowMs() + rest
			events = append(events, wire.SourceEvent{Ts: end, Ev: wire.AgentStatusChange{AgentID: a.agent.ID, Status: "idle"}})
		}
		s.send(events)
	})
	return out
}

func pick(first bool, a, b string) string {
	if first {
		return a
	}
	return b
}
