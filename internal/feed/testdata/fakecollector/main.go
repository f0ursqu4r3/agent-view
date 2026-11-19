// Command fakecollector stands in for the TypeScript collector in tests. It
// writes the lines of a file to stdout, then waits until a signal ends it.
//
// Usage: fakecollector [flags] [file]
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	exitAfter := flag.Int("exit-after", -1, "exit with status 3 after this many lines")
	stderrText := flag.String("stderr", "", "write this line to stderr first")
	big := flag.Bool("big", false, "write one 200 KiB snapshot line first")
	pidFile := flag.String("pidfile", "", "write the process id to this file")
	ignoreTerm := flag.Bool("ignore-term", false, "ignore SIGTERM")
	delay := flag.Duration("delay", 0, "pause between lines")
	flag.Parse()

	if *ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if *stderrText != "" {
		fmt.Fprintln(os.Stderr, *stderrText)
	}

	var lines []string
	if *big {
		lines = append(lines, bigSnapshot())
	}
	if path := flag.Arg(0); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
	}

	out := bufio.NewWriter(os.Stdout)
	for i, l := range lines {
		if *exitAfter >= 0 && i >= *exitAfter {
			break
		}
		fmt.Fprintln(out, l)
		out.Flush()
		if *delay > 0 {
			time.Sleep(*delay)
		}
	}
	if *exitAfter >= 0 {
		os.Exit(3)
	}
	time.Sleep(24 * time.Hour)
}

func bigSnapshot() string {
	var b strings.Builder
	b.WriteString(`{"v":1,"host":"big","seq":1,"kind":"snapshot","agents":[],"openCalls":[],"files":[`)
	for i := 0; b.Len() < 200*1024; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"path":"/some/long/path/to/file-%06d.ts","lastTouchAt":"2026-01-01T00:00:00.000Z","attribution":"unattributed"}`, i)
	}
	b.WriteString(`]}`)
	return b.String()
}
