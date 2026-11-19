package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agent-view/internal/cli"
	"agent-view/internal/collect"
	"agent-view/internal/hub"
	"agent-view/internal/lifecycle"
	"agent-view/internal/remote"
	"agent-view/internal/server"
)

// localLabel is the host name up to its first dot, or "local".
func localLabel() string {
	h, _ := os.Hostname()
	label, _, _ := strings.Cut(h, ".")
	if label == "" {
		return "local"
	}
	return label
}

// runServe ports runHub of src/cli.ts: the local collector, the remotes and
// the snapshot server behind one hub. It returns the exit code: 0 after a
// signal on signals, 1 when the server cannot start, 2 for a bad
// AGENT_VIEW_REMOTE_COMMAND. After a signal it stops the server and the hub;
// when the hub has not stopped within stopWait, it sends SIGKILL to every
// remote child and returns.
func runServe(args cli.Args, stdout, stderr io.Writer, signals <-chan os.Signal) int {
	override, err := cli.RemoteCommandOverride(os.Getenv("AGENT_VIEW_REMOTE_COMMAND"))
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 2
	}
	var command func(sshHost, label string) []string
	if override != nil {
		command = func(string, string) []string { return override }
	}

	lc := lifecycle.Options{LingerMs: args.LingerMs}
	h := hub.New(hub.Options{Lifecycle: lc})
	// fail stops the hub, which stops every source it holds.
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "agent-view: "+format+"\n", a...)
		h.Stop()
		return 1
	}

	label := h.UniqueLabel(localLabel())
	local := hub.NewLocalSource(collect.New(collect.Options{
		Host:      label,
		Demo:      args.Demo,
		DemoScale: args.DemoScale,
		Watch:     args.Watch,
		Lifecycle: lc,
	}), label)
	if err := h.AddSource(local); err != nil {
		return fail("%v", err)
	}
	remotes := make([]*remote.Remote, 0, len(args.Remotes))
	for _, sshHost := range args.Remotes {
		r, err := remote.New(remote.Options{SSHHost: sshHost, Label: h.UniqueLabel(sshHost), Command: command})
		if err != nil {
			return fail("remote %s: %v", sshHost, err)
		}
		if err := h.AddSource(r); err != nil {
			return fail("remote %s: %v", sshHost, err)
		}
		remotes = append(remotes, r)
	}

	srv, err := server.Start(h, args.Port)
	if err != nil {
		return fail("cannot start the server on port %d: %v", args.Port, err)
	}
	fmt.Fprintf(stdout, "Agent View: %s\n", srv.URL)

	// A remote that fails to connect only shows as down. It never stops the
	// hub or the local collector.
	for _, r := range remotes {
		if err := r.Start(); err != nil {
			fmt.Fprintf(stderr, "agent-view: remote %s failed: %v\n", r.Label(), err)
		}
	}
	// Start waits for the first scan, so it runs off this goroutine: a signal
	// must be able to end the process meanwhile.
	go func() {
		if err := local.Start(); err != nil {
			fmt.Fprintf(stderr, "agent-view: local collector failed: %v\n", err)
		}
	}()

	<-signals
	srv.Stop()
	stopped := make(chan struct{})
	go func() {
		h.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(stopWait):
		// A remote child that ignores SIGTERM gets SIGKILL from its Remote
		// only 2 s after SIGTERM, when this process has already exited.
		for _, r := range remotes {
			r.Kill()
		}
	}
	return 0
}
