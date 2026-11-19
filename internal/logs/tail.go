package logs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
)

// TailLimit is how much of a large log the tailer loads: its header line and
// its last 4 MiB.
const TailLimit = 4 << 20

const mib = 1 << 20

// LineSplitter splits a byte stream into complete lines. It decodes only
// complete lines, so a UTF-8 character that arrives in two chunks is safe.
// Invalid bytes become U+FFFD, as with TextDecoder.
type LineSplitter struct {
	buf []byte
}

// Push adds a chunk and returns the lines it completed. It does not keep chunk.
func (l *LineSplitter) Push(chunk []byte) []string {
	merged := chunk
	if len(l.buf) > 0 {
		merged = make([]byte, 0, len(l.buf)+len(chunk))
		merged = append(append(merged, l.buf...), chunk...)
	}
	var lines []string
	start := 0
	for {
		i := bytes.IndexByte(merged[start:], '\n')
		if i < 0 {
			break
		}
		lines = append(lines, decodeUTF8(merged[start:start+i]))
		start += i + 1
	}
	// Copy the rest: the buffer must not pin the whole chunk.
	if rest := merged[start:]; len(rest) > 0 {
		l.buf = bytes.Clone(rest)
	} else {
		l.buf = nil
	}
	return lines
}

// Reset drops the incomplete line.
func (l *LineSplitter) Reset() { l.buf = nil }

// PendingBytes is the size of the incomplete line.
func (l *LineSplitter) PendingBytes() int { return len(l.buf) }

// decodeUTF8 decodes like `new TextDecoder().decode(b)`: a leading BOM is
// dropped, and each maximal subpart of an ill-formed sequence becomes one
// U+FFFD (WHATWG Encoding).
func decodeUTF8(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		var need int
		lo, hi := byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			need = 1
		case c == 0xe0:
			need, lo = 2, 0xa0
		case c == 0xed:
			need, hi = 2, 0x9f
		case c >= 0xe1 && c <= 0xef:
			need = 2
		case c == 0xf0:
			need, lo = 3, 0x90
		case c == 0xf4:
			need, hi = 3, 0x8f
		case c >= 0xf1 && c <= 0xf3:
			need = 3
		default:
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		ok := true
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lo || b[j] > hi {
				ok = false
				break
			}
			lo, hi = 0x80, 0xbf
			j++
		}
		if ok {
			sb.Write(b[i:j])
		} else {
			sb.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return sb.String()
}

type cursor struct {
	offset int64
	ino    uint64
	split  LineSplitter
	// skipFirst discards the next complete line: it is the rest of a cut or oversized record.
	skipFirst bool
}

// Tailer reads appended bytes from JSONL logs and emits each decoded record.
// It keeps one cursor per file. A large file loads only its header line and
// its last limit bytes. A Tailer is not safe for concurrent use.
type Tailer struct {
	onRecord  func(record any, file string)
	onWarning func(string)
	limit     int
	cursors   map[string]*cursor
	malformed int
}

// NewTailer returns a Tailer. A limit of 0 or less means TailLimit. onWarning may be nil.
func NewTailer(onRecord func(record any, file string), limit int, onWarning func(string)) *Tailer {
	if limit <= 0 {
		limit = TailLimit
	}
	if onWarning == nil {
		onWarning = func(string) {}
	}
	return &Tailer{onRecord: onRecord, onWarning: onWarning, limit: limit, cursors: map[string]*cursor{}}
}

// OpenFiles is the number of files with a cursor.
func (t *Tailer) OpenFiles() int { return len(t.cursors) }

// Malformed is the number of non-empty lines that were not valid JSON.
func (t *Tailer) Malformed() int { return t.malformed }

// Forget drops the cursor of file.
func (t *Tailer) Forget(file string) { delete(t.cursors, file) }

func describeLimit(limit int) string {
	if limit%mib == 0 {
		return fmt.Sprintf("%d MiB", limit/mib)
	}
	return fmt.Sprintf("%d bytes", limit)
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// Read emits the records appended to file since the last read. A stat or open
// error is returned, never reported as a warning.
func (t *Tailer) Read(file string) error {
	fi, err := os.Stat(file)
	if err != nil {
		return err
	}
	size := fi.Size()
	ino := inode(fi)
	c := t.cursors[file]
	if c == nil || c.ino != ino || size < c.offset {
		c = &cursor{ino: ino}
		t.cursors[file] = c
		if size > int64(t.limit) {
			if err := t.readHeader(file); err != nil {
				return err
			}
			c.offset = size - int64(t.limit)
			c.skipFirst = true
			t.onWarning("Large logs load only their header and last 4 MiB. Earlier tool calls may be missing.")
		}
	}
	if size == c.offset {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var data []byte
	for c.offset < size {
		n := min(int64(t.limit), size-c.offset)
		if int64(len(data)) < n {
			data = make([]byte, n)
		}
		read, err := f.ReadAt(data[:n], c.offset)
		if read == 0 {
			if err != nil && err != io.EOF {
				return err
			}
			break
		}
		c.offset += int64(read)
		t.feed(file, c, data[:read])
	}
	return nil
}

func (t *Tailer) readHeader(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, t.limit)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return err
	}
	if end := bytes.IndexByte(head[:n], '\n'); end >= 0 {
		t.parse(file, decodeUTF8(head[:end]))
	}
	return nil
}

func (t *Tailer) feed(file string, c *cursor, chunk []byte) {
	for _, line := range c.split.Push(chunk) {
		if c.skipFirst {
			c.skipFirst = false
		} else {
			t.parse(file, line)
		}
	}
	if c.split.PendingBytes() > t.limit {
		c.split.Reset()
		// Warn once per record: skipFirst is already set while still inside the same oversized record.
		if !c.skipFirst {
			t.onWarning(fmt.Sprintf("A log record exceeded %s and was skipped.", describeLimit(t.limit)))
		}
		c.skipFirst = true
	}
}

func (t *Tailer) parse(file, line string) {
	if line == "" {
		return
	}
	record, err := Decode(line)
	if err != nil {
		t.malformed++
		return
	}
	t.onRecord(record, file)
}
