package watch

import (
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsevents"
)

// A batch with MustScanSubDirs|UserDropped on the root sets "events dropped
// under load". The root event is not reported; a file event reports Rename.
func TestFSEventsDropFlags(t *testing.T) {
	r := newRecorder(t)
	s, failed := start(t.TempDir(), r.onChange, Options{})
	if failed != nil {
		t.Fatalf("start: %q", failed.Degraded())
	}
	x := &fseventsWatcher{state: s}
	x.handle([]fsevents.Event{
		{Path: s.real, Flags: fsevents.MustScanSubDirs | fsevents.UserDropped},
		{Path: filepath.Join(s.real, "sub", "f.txt"), Flags: fsevents.ItemModified | fsevents.ItemIsFile},
	})
	if got, want := x.Degraded(), "events dropped under load"; got != want {
		t.Errorf("Degraded() = %q, want %q", got, want)
	}
	if x.Partial() {
		t.Error("Partial() = true after dropped events, want false")
	}
	want := []event{{filepath.Join("sub", "f.txt"), Rename}}
	if got := r.events(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("events = %v, want %v", got, want)
	}
}
