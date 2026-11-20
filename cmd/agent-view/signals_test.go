package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// signalWait bounds each wait for a signal to reach the watcher.
const signalWait = 5 * time.Second

// watchForTest registers a watcher for the test and stops it at cleanup. Only
// one watcher is registered at a time: the tests in this file do not run in
// parallel.
func watchForTest(t *testing.T) *shutdownSignals {
	t.Helper()
	s := watchShutdownSignals()
	t.Cleanup(s.Stop)
	return s
}

// raise sends sig to this test process and waits until the watcher has read
// it. A signal that is still on its way when Stop runs would get the default
// action and end the test process.
func raise(t *testing.T, s *shutdownSignals, sig syscall.Signal) {
	t.Helper()
	before := s.read.Load()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(signalWait); s.read.Load() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher did not read %v within %v", sig, signalWait)
		}
	}
}

func requested(s *shutdownSignals) bool {
	select {
	case <-s.Requested():
		return true
	default:
		return false
	}
}

// The first INT, TERM or HUP closes Requested. The watcher is registered, so
// the signal does not end the test process.
func TestShutdownSignalsFirstSignalClosesRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{{"INT", syscall.SIGINT}, {"TERM", syscall.SIGTERM}, {"HUP", syscall.SIGHUP}} {
		t.Run(tc.name, func(t *testing.T) {
			s := watchForTest(t)
			if requested(s) {
				t.Fatal("Requested is closed before any signal")
			}
			raise(t, s, tc.sig)
			select {
			case <-s.Requested():
			case <-time.After(signalWait):
				t.Fatalf("Requested not closed within %v after %v", signalWait, tc.sig)
			}
		})
	}
}

// Signals after the first are read and ignored until Stop: no panic (a second
// close), Requested stays closed and Stop returns.
func TestShutdownSignalsRepeatsAreIgnored(t *testing.T) {
	s := watchForTest(t)
	raise(t, s, syscall.SIGTERM)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		raise(t, s, sig)
		if !requested(s) {
			t.Fatalf("Requested not closed after %v", sig)
		}
	}
	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(signalWait):
		t.Fatalf("Stop did not return within %v", signalWait)
	}
	if !requested(s) {
		t.Fatal("Requested not closed after Stop")
	}
}

// Stop is idempotent: a second call returns at once, also with no signal.
func TestShutdownSignalsStopTwice(t *testing.T) {
	s := watchShutdownSignals()
	s.Stop()
	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(signalWait):
		t.Fatalf("the second Stop did not return within %v", signalWait)
	}
	if requested(s) {
		t.Fatal("Requested closed without a signal")
	}
}
