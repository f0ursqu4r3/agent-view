package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"agent-view/internal/cli"
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

// runServe ports runHub of the former TypeScript cli.ts (removed in P2c; see
// git history): the local collector, the remotes and the snapshot server
// behind one hub. It returns the exit code: 0 after a
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

	st, err := newStack(args, override, nil)
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: %v\n", err)
		return 1
	}

	srv, err := server.Start(st.hub, args.Port)
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: cannot start the server on port %d: %v\n", args.Port, err)
		st.hub.Stop() // no source has started: this does not wait
		return 1
	}
	fmt.Fprintf(stdout, "Agent View: %s\n", srv.URL)

	st.start(func(msg string) { fmt.Fprintf(stderr, "agent-view: %s\n", msg) })

	<-signals
	srv.Stop()
	st.stop(stopWait)
	return 0
}
