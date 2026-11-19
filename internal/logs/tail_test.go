package logs

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// toPlain converts decoded values to maps and slices so tests can compare them.
func toPlain(v any) any {
	switch x := v.(type) {
	case *Obj:
		m := map[string]any{}
		for _, e := range x.Entries() {
			m[e.Key] = toPlain(e.Val)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = toPlain(e)
		}
		return out
	}
	return v
}

type tailRig struct {
	t        *Tailer
	records  []any
	warnings []string
}

func newRig(limit int) *tailRig {
	r := &tailRig{}
	r.t = NewTailer(func(rec any, _ string) { r.records = append(r.records, toPlain(rec)) }, limit, func(m string) { r.warnings = append(r.warnings, m) })
	return r
}

func write(t *testing.T, file string, data []byte) {
	t.Helper()
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, file string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, r *tailRig, file string) {
	t.Helper()
	if err := r.t.Read(file); err != nil {
		t.Fatal(err)
	}
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

type m = map[string]any

// tailer waits for complete JSON lines and preserves split UTF-8
func TestTailWaitsForCompleteLinesAndPreservesSplitUTF8(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(0)
	line := []byte(`{"type":"session_meta","payload":{"id":"test","cwd":"/project/日本"}}` + "\n")
	cut := bytes.Index(line, []byte("日")) + 1
	write(t, file, line[:cut])
	read(t, r, file)
	eq(t, len(r.records), 0)
	appendTo(t, file, line[cut:])
	read(t, r, file)
	eq(t, r.records, []any{m{"type": "session_meta", "payload": m{"id": "test", "cwd": "/project/日本"}}})
	eq(t, r.t.Malformed(), 0)
	read(t, r, file)
	eq(t, len(r.records), 1)
	eq(t, r.t.OpenFiles(), 1)
	r.t.Forget(file)
	eq(t, r.t.OpenFiles(), 0)
}

// replaced file (new inode) and truncated file restart from offset 0
func TestTailReplacedAndTruncatedFileRestart(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	r := newRig(0)
	write(t, file, []byte("{\"n\":1}\n{\"n\":2}\n"))
	read(t, r, file)
	eq(t, r.records, []any{m{"n": 1.0}, m{"n": 2.0}})
	// Replace: write a new file and rename over the old one (new inode, larger size).
	other := filepath.Join(dir, "b.jsonl")
	write(t, other, []byte("{\"n\":3}\n{\"n\":4}\n{\"n\":5}\n"))
	if err := os.Rename(other, file); err != nil {
		t.Fatal(err)
	}
	read(t, r, file)
	eq(t, r.records[2:], []any{m{"n": 3.0}, m{"n": 4.0}, m{"n": 5.0}})
	// Truncate in place (same inode, smaller size).
	if err := os.Truncate(file, 0); err != nil {
		t.Fatal(err)
	}
	write(t, file, []byte("{\"n\":6}\n"))
	read(t, r, file)
	eq(t, r.records[5:], []any{m{"n": 6.0}})
}

// large file loads first line plus last limit bytes and skips the cut line
func TestTailLargeFileLoadsHeaderAndLastLimitBytes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "big.jsonl")
	r := newRig(1024)
	mk := func(i int) string {
		head := `{"i":` + strconv.Itoa(i) + `,"pad":"`
		tail := `"}` + "\n"
		return head + strings.Repeat("x", 100-len(head)-len(tail)) + tail
	}
	header := `{"header":true}` + "\n"
	var lines []string
	for i := 0; i < 50; i++ {
		l := mk(i)
		if len(l) != 100 {
			t.Fatalf("line %d is %d bytes", i, len(l))
		}
		lines = append(lines, l)
	}
	write(t, file, []byte(header+strings.Join(lines, "")))
	size := len(header) + 5000
	offset := size - 1024
	read(t, r, file)
	// The first line that starts after the offset is the first line after the cut line.
	firstFull := -1
	for i := range lines {
		if len(header)+i*100 > offset {
			firstFull = i
			break
		}
	}
	var expected []any
	for i := firstFull; i < 50; i++ {
		expected = append(expected, float64(i))
	}
	eq(t, r.records[0], m{"header": true})
	var got []any
	for _, rec := range r.records[1:] {
		got = append(got, rec.(m)["i"])
	}
	eq(t, got, expected)
	eq(t, len(r.records), 1+(50-firstFull))
	eq(t, r.warnings, []string{"Large logs load only their header and last 4 MiB. Earlier tool calls may be missing."})
	// Another read adds nothing and does not warn again.
	read(t, r, file)
	eq(t, len(r.warnings), 1)
	eq(t, len(r.records), 1+(50-firstFull))
}

// malformed lines are counted, not thrown
func TestTailMalformedLinesAreCounted(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(0)
	write(t, file, []byte("{\"a\":1}\n{oops\n\n{\"a\":2}\n"))
	read(t, r, file)
	eq(t, r.records, []any{m{"a": 1.0}, m{"a": 2.0}})
	eq(t, r.t.Malformed(), 1)
}

// a record larger than the limit is skipped with one warning
func TestTailRecordLargerThanLimitSkipped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(1024)
	huge := `{"blob":"` + strings.Repeat("y", 5000) + `"}` + "\n"
	write(t, file, []byte("{\"a\":1}\n"+huge[:900]))
	read(t, r, file)
	appendTo(t, file, []byte(huge[900:]+"{\"a\":2}\n"))
	read(t, r, file)
	eq(t, r.records, []any{m{"a": 1.0}, m{"a": 2.0}})
	eq(t, r.warnings, []string{"A log record exceeded 1024 bytes and was skipped."})
	eq(t, r.t.Malformed(), 0)
}

// LineSplitter returns only complete lines and keeps split UTF-8
func TestLineSplitterCompleteLinesOnly(t *testing.T) {
	var s LineSplitter
	b := []byte("{\"p\":\"日本\"}\n{\"q\":1}")
	const cut = 8 // inside the multi-byte character
	eq(t, len(s.Push(b[:cut])), 0)
	eq(t, s.Push(b[cut:]), []string{`{"p":"日本"}`})
	eq(t, s.PendingBytes(), 7)
	eq(t, s.Push([]byte("\n")), []string{`{"q":1}`})
	eq(t, s.PendingBytes(), 0)
	s.Push([]byte("abc"))
	s.Reset()
	eq(t, s.PendingBytes(), 0)
}

// Review Focus 4: a partial last line waits for its newline.
func TestTailPartialLineWaits(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(0)
	write(t, file, []byte("{\"a\":1}\n{\"b\":"))
	read(t, r, file)
	eq(t, r.records, []any{m{"a": 1.0}})
	appendTo(t, file, []byte("2}\n"))
	read(t, r, file)
	eq(t, r.records, []any{m{"a": 1.0}, m{"b": 2.0}})
	eq(t, r.t.Malformed(), 0)
}

// Review Focus 5: invalid UTF-8 becomes U+FFFD.
func TestTailInvalidUTF8(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(0)
	write(t, file, []byte("{\"s\":\"a\xff\xfeb\"}\n"))
	read(t, r, file)
	eq(t, r.records, []any{m{"s": "a��b"}})
	eq(t, r.t.Malformed(), 0)
}

func TestLineSplitterInvalidUTF8(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\xff\xfe", "��"},
		{"\xe2\x82A", "�A"},
		{"\xe2\x82", "�"},
		{"\xe2", "�"},
		{"\xe2\x82\xac", "€"},
		{"\xed\xa0\x80", "���"}, // an encoded surrogate: ED then two invalid bytes
		{"\xf0\x9f\x98", "�"},
		{"\xf0\x9f\x98\x80", "😀"},
		{"\xc0\x80", "��"},
		{"\x80\x80", "��"},
		{"\xf4\x90\x80\x80", "����"},
		{"a\xe2\x82\xe2\x82\xacb", "a�€b"},
		{"\xef\xbb\xbf{}", "{}"}, // TextDecoder strips a leading BOM
	}
	for _, c := range cases {
		var s LineSplitter
		got := s.Push([]byte(c.in + "\n"))
		if !slices.Equal(got, []string{c.want}) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTailMissingFileReturnsError(t *testing.T) {
	r := newRig(0)
	err := r.t.Read(filepath.Join(t.TempDir(), "gone.jsonl"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("got %v, want a not-exist error", err)
	}
	eq(t, len(r.warnings), 0)
	eq(t, r.t.OpenFiles(), 0)
}

func TestTailHeaderWithoutNewlineEmitsNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.jsonl")
	r := newRig(16)
	write(t, file, []byte(strings.Repeat("x", 64)))
	read(t, r, file)
	eq(t, len(r.records), 0)
	eq(t, r.warnings, []string{"Large logs load only their header and last 4 MiB. Earlier tool calls may be missing."})
}

func TestTailOversizedWarningUsesMiB(t *testing.T) {
	if describeLimit(2<<20) != "2 MiB" || describeLimit(1024) != "1024 bytes" || describeLimit(TailLimit) != "4 MiB" {
		t.Error("describeLimit")
	}
}
