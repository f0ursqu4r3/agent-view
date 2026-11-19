// Throwaway spike (P2a): file watching for the Go collector.
//
// Compares FSEvents (github.com/fsnotify/fsevents, cgo, darwin only) with
// fsnotify (kqueue on darwin, inotify on linux). The probe repeats the M0
// scenarios of docs/superpowers/spikes/2026-11-17-m0-results.md; the scale
// mode measures setup time, descriptors, memory, idle CPU, and the cost of
// churn in an ignored directory.
//
//	fswatch probe [flags]          M0 scenarios, every class and backend
//	fswatch scale [flags]          one large tree, one backend
//	fswatch count <root>...        dirs and files per root, ignored dirs skipped
//	fswatch write <root> <n> <ms> <subdirs>   external writer (used by probe)
//	fswatch churn <dir> <n>        external churn writer (used by scale)
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "probe":
		probeMain(args)
	case "scale":
		scaleMain(args)
	case "count":
		countMain(args)
	case "write":
		writeMain(args)
	case "churn":
		churnMain(args)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fswatch probe|scale|count|write|churn ...")
	os.Exit(2)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "fswatch: "+format+"\n", a...)
	os.Exit(1)
}
