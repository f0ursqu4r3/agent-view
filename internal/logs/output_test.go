package logs

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-view/internal/wire"
)

func outputs(events []wire.SourceEvent) []wire.ToolOutput {
	var out []wire.ToolOutput
	for _, e := range events {
		out = append(out, e.Ev.(wire.ToolOutput))
	}
	return out
}

func TestOutputEventsKeepsCharacters(t *testing.T) {
	// 3,333 x (é 2 bytes + 😀 4 bytes) + é = 20,000 bytes.
	text := strings.Repeat("é😀", 3333) + "é"
	if len(text) != 20000 {
		t.Fatalf("setup: %d bytes", len(text))
	}
	events := OutputEvents("T", "a", "c", text)
	chunks := outputs(events)
	var joined strings.Builder
	for i, c := range chunks {
		if len(c.Chunk) > MaxChunkBytes || !utf8.ValidString(c.Chunk) || strings.ContainsRune(c.Chunk, utf8.RuneError) {
			t.Errorf("chunk %d: %d bytes, valid=%v", i, len(c.Chunk), utf8.ValidString(c.Chunk))
		}
		if c.Truncated != (i == 0) {
			t.Errorf("chunk %d: truncated = %v", i, c.Truncated)
		}
		if c.AgentID != "a" || c.CallID != "c" || events[i].Ts != "T" {
			t.Errorf("chunk %d: %+v ts=%s", i, c, events[i].Ts)
		}
		joined.WriteString(c.Chunk)
	}
	start := len(text) - MaxOutputBytes
	for !utf8.RuneStart(text[start]) {
		start++
	}
	if joined.String() != text[start:] {
		t.Errorf("joined %d bytes, want the last 16 KiB from a character boundary (%d bytes)", joined.Len(), len(text)-start)
	}
	if got := OutputEvents("T", "a", "c", ""); len(got) != 0 {
		t.Errorf("empty text gave %d events", len(got))
	}
}

func TestOutputEventsInvalidUTF8(t *testing.T) {
	text := "ok\xff\xfe" + strings.Repeat("a", 5000) + "\xc3"
	var joined strings.Builder
	for _, c := range outputs(OutputEvents("T", "a", "c", text)) {
		if !utf8.ValidString(c.Chunk) || len(c.Chunk) > MaxChunkBytes {
			t.Fatalf("bad chunk: %d bytes, valid=%v", len(c.Chunk), utf8.ValidString(c.Chunk))
		}
		joined.WriteString(c.Chunk)
	}
	if want := "ok��" + strings.Repeat("a", 5000) + "�"; joined.String() != want {
		t.Errorf("joined = %q", joined.String())
	}
}

func TestChunkBytesTakesFullSizeWhenNoBoundary(t *testing.T) {
	// 'a' then ten stray continuation bytes. Every cut falls on a continuation
	// byte and stepping back reaches the chunk start, so each chunk takes the
	// full 4 bytes (3 for the last). Each stray byte becomes one U+FFFD.
	buf := append([]byte{'a'}, bytes.Repeat([]byte{0x80}, 10)...)
	got := chunkBytes(buf, 4)
	want := []string{"a" + strings.Repeat("\uFFFD", 3), strings.Repeat("\uFFFD", 4), strings.Repeat("\uFFFD", 3)}
	if !slices.Equal(got, want) {
		t.Errorf("chunks = %q, want %q", got, want)
	}
}

func TestResolveFile(t *testing.T) {
	cwd := "/project"
	rel := "rel/dir"
	cases := []struct {
		cwd  *string
		file any
		want string
		ok   bool
	}{
		{&cwd, "src/a.ts", "/project/src/a.ts", true},
		{&cwd, "/other//b/../c.ts/", "/other/c.ts", true},
		{nil, "/abs", "/abs", true},
		{nil, "src/a.ts", "", false},
		{&cwd, "", "", false},
		{&cwd, 5.0, "", false},
		{&cwd, nil, "", false},
		{&cwd, "../up.txt", "/up.txt", true},
	}
	for _, c := range cases {
		got, ok := resolveFile(c.cwd, c.file)
		if got != c.want || ok != c.ok {
			t.Errorf("resolveFile(%v, %v) = %q, %v", c.cwd, c.file, got, ok)
		}
	}
	got, ok := resolveFile(&rel, "x.txt")
	if !ok || !strings.HasSuffix(got, "/rel/dir/x.txt") || got[0] != '/' {
		t.Errorf("relative cwd: %q, %v", got, ok)
	}
}
