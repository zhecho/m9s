package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logTime is how an entry's time is written in the log file.
const logTime = "2006-01-02 15:04:05"

// FormatPlain is e as one line of the log file, as the log view shows it
// without colours: "2026-11-26 09:24:23 ERROR message". A line break in
// the message becomes ⏎, so an entry stays one line.
func FormatPlain(e Entry) string {
	msg := strings.ReplaceAll(strings.ReplaceAll(e.Msg, "\r\n", "\n"), "\n", "⏎")
	return fmt.Sprintf("%s %-5s %s", e.Time.Format(logTime), e.Level, msg)
}

// ParsePlain reads one line of the log file back. A line that is not in
// its format (edited by hand, cut short) is an INFO entry with the line as
// its message and no time.
func ParsePlain(line string) Entry {
	garbage := Entry{Level: LevelInfo, Msg: line}
	if len(line) < len(logTime)+7 || line[len(logTime)] != ' ' {
		return garbage
	}
	t, err := time.ParseInLocation(logTime, line[:len(logTime)], time.Local)
	if err != nil {
		return garbage
	}
	rest := line[len(logTime)+1:]
	word, msg, ok := strings.Cut(rest, " ")
	if !ok {
		return garbage
	}
	var level Level
	switch word {
	case "INFO":
		level, msg = LevelInfo, strings.TrimPrefix(msg, " ") // padded to five
	case "WARN":
		level, msg = LevelWarn, strings.TrimPrefix(msg, " ")
	case "ERROR":
		level = LevelError
	default:
		return garbage
	}
	return Entry{Time: t, Level: level, Msg: msg}
}

// TailEntries reads the last n lines of the log file at path; nil when
// there is none.
func TailEntries(path string, n int) []Entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSuffix(line, "\r"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	lines = lines[max(len(lines)-n, 0):]
	if len(lines) == 0 {
		return nil
	}
	out := make([]Entry, len(lines))
	for i, line := range lines {
		out[i] = ParsePlain(line)
	}
	return out
}

// LogFile appends the log to a file that is kept across runs. Past maxBytes
// it moves to path+".1", replacing the previous one, and starts anew;
// maxBytes 0 never rotates (an m9s command: only the mail app rotates, so
// two processes never rotate at once).
//
// The mail app also reads what other processes (m9s commands) append, with
// ReadNew: it skips its own lines, which it remembers in the order it wrote
// them — appends from two processes interleave, so a byte position cannot
// tell them apart.
type LogFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	closed   bool        // Close was called: no more writes
	onError  func(error) // told of the first failure (once per run)
	warned   bool

	offset  int64    // bytes of the file read (or there when it was opened)
	own     []string // lines written here and not yet read back
	pending []Entry  // other processes' lines read during a write
}

// ownMax bounds the lines remembered for ReadNew when nobody reads.
const ownMax = 10000

// OnError sets fn to be told, once, that the file could not be written. fn
// runs outside the file's lock, so it may log.
func (lf *LogFile) OnError(fn func(error)) {
	lf.mu.Lock()
	lf.onError = fn
	lf.mu.Unlock()
}

// OpenLogFile opens (or creates) the log file at path, private to the user
// (0600, in a 0700 directory made when missing), for appending.
func OpenLogFile(path string, maxBytes int64) (*LogFile, error) {
	lf := &LogFile{path: path, maxBytes: maxBytes}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := lf.open(); err != nil {
		return nil, err
	}
	return lf, nil
}

func (lf *LogFile) open() error {
	f, err := os.OpenFile(lf.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	lf.f = f
	if st, err := f.Stat(); err == nil {
		lf.offset = st.Size() // what is there already was loaded, not news
	}
	lf.own = nil
	return nil
}

// ReadNew returns the complete lines other processes appended since the
// last call, oldest first.
func (lf *LogFile) ReadNew() []Entry {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	lf.readNew()
	out := lf.pending
	lf.pending = nil
	return out
}

// readNew reads the complete lines past offset into pending, skipping the
// ones written here. lf.mu is held.
func (lf *LogFile) readNew() {
	st, err := os.Stat(lf.path)
	if err != nil {
		return
	}
	if st.Size() < lf.offset { // emptied or replaced: start over
		lf.offset, lf.own = 0, nil
	}
	if st.Size() == lf.offset {
		return
	}
	f, err := os.Open(lf.path)
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, st.Size()-lf.offset)
	n, _ := f.ReadAt(buf, lf.offset)
	buf = buf[:n]
	end := strings.LastIndexByte(string(buf), '\n')
	if end < 0 {
		return // a line still being written
	}
	lf.offset += int64(end + 1)
	for _, line := range strings.Split(string(buf[:end]), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(lf.own) > 0 && line == lf.own[0] {
			lf.own = lf.own[1:]
			continue
		}
		if strings.TrimSpace(line) != "" {
			lf.pending = append(lf.pending, ParsePlain(line))
		}
	}
}

// Follow calls deliver with the lines other processes append, every
// interval, until stop is called.
func (lf *LogFile) Follow(interval time.Duration, deliver func([]Entry)) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if es := lf.ReadNew(); len(es) > 0 {
					deliver(es)
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// Write appends e as one line, and rotates the file when it has grown past
// the cap. A failed write is dropped — the log must never stop m9s — said
// once (OnError), and the file is opened again for the next line.
func (lf *LogFile) Write(e Entry) {
	if err := lf.write(e); err != nil {
		lf.mu.Lock()
		fn, first := lf.onError, !lf.warned
		lf.warned = true
		lf.mu.Unlock()
		if fn != nil && first {
			fn(err)
		}
	}
}

func (lf *LogFile) write(e Entry) error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.closed {
		return nil
	}
	if lf.f == nil { // an earlier write failed: try the file again
		if err := lf.open(); err != nil {
			return err
		}
	}
	line := FormatPlain(e)
	if _, err := lf.f.WriteString(line + "\n"); err != nil {
		lf.f.Close()
		lf.f = nil
		return err
	}
	if lf.maxBytes > 0 { // the mail app: remember its own lines for ReadNew
		lf.own = append(lf.own, line)
		if len(lf.own) > ownMax {
			lf.own = lf.own[len(lf.own)-ownMax:]
		}
	}
	if st, err := lf.f.Stat(); err == nil && lf.maxBytes > 0 && st.Size() > lf.maxBytes {
		lf.readNew() // what others added to the old file is not lost
		lf.f.Close()
		lf.f = nil
		if err := os.Rename(lf.path, lf.path+".1"); err != nil {
			// The old log cannot be moved aside: empty it instead, so the
			// file never grows past the cap.
			_ = os.Truncate(lf.path, 0)
		}
		return lf.open()
	}
	return nil
}

// Close closes the file; later writes are dropped.
func (lf *LogFile) Close() error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	lf.closed = true
	if lf.f == nil {
		return nil
	}
	err := lf.f.Close()
	lf.f = nil
	return err
}
