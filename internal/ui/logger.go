package ui

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

// Level is how serious a log entry is.
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

// String returns the level word shown in the log view.
func (lv Level) String() string {
	switch lv {
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Entry is one log line.
type Entry struct {
	Time  time.Time
	Level Level
	Msg   string
}

// Logger is a thread-safe in-memory log buffer.
type Logger struct {
	mu      sync.Mutex
	entries []Entry
	notify  []func()
}

// AppLog is the global application logger.
var AppLog = &Logger{}

// Add appends an entry at level, stamped with the current time.
func (l *Logger) Add(level Level, format string, args ...any) {
	e := Entry{Time: time.Now(), Level: level, Msg: fmt.Sprintf(format, args...)}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	cbs := make([]func(), len(l.notify))
	copy(cbs, l.notify)
	l.mu.Unlock()
	for _, fn := range cbs {
		fn()
	}
}

// Info logs at LevelInfo.
func (l *Logger) Info(format string, args ...any) { l.Add(LevelInfo, format, args...) }

// Warn logs at LevelWarn.
func (l *Logger) Warn(format string, args ...any) { l.Add(LevelWarn, format, args...) }

// Error logs at LevelError.
func (l *Logger) Error(format string, args ...any) { l.Add(LevelError, format, args...) }

// Entries returns a snapshot of all log entries.
func (l *Logger) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// OnUpdate registers a callback invoked whenever a new entry is added.
func (l *Logger) OnUpdate(fn func()) {
	l.mu.Lock()
	l.notify = append(l.notify, fn)
	l.mu.Unlock()
}

// levelRole maps a level to the skin role its row is drawn in.
var levelRole = map[Level]string{
	LevelInfo:  "foreground",
	LevelWarn:  "warn",
	LevelError: "flash_err",
}

// FormatEntry renders e as one tview-tagged log row: the date and time in
// the dim colour, then the level and message in the level's colour.
func FormatEntry(e Entry, s *Skin) string {
	role := levelRole[e.Level]
	if s.Colors[role] == "" {
		role = "foreground" // a skin without the role still reads
	}
	return fmt.Sprintf("[%s::]%s[-:-:-] [%s::]%-5s %s[-:-:-]",
		s.ColorName("dim"), e.Time.Format("2006-01-02 15:04:05"),
		s.ColorName(role), e.Level, escapeTags(e.Msg))
}

// tagPattern matches what tview reads as a colour tag ([fg:bg:attr]); it is
// tview's own colorPattern.
var tagPattern = regexp.MustCompile(`\[([a-zA-Z]+|#[0-9a-zA-Z]{6}|\-)?:([a-zA-Z]+|#[0-9a-zA-Z]{6}|\-)?:([lbdru]+|\-)?\]`)

// escapeTags escapes only the colour tags in msg. tview.Escape would escape
// every bracket, and tview's word wrap garbles an escape it breaks inside;
// brackets that are not tags ([work], [PATCH v2]) already print as they are.
func escapeTags(msg string) string {
	return tagPattern.ReplaceAllStringFunc(msg, func(tag string) string {
		return tag[:len(tag)-1] + "[]"
	})
}
