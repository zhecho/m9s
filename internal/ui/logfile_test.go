package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Entries written to the file come back with their time, level and text;
// a message's line breaks stay on one line.
func TestLogFile_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "m9s.log")
	lf, err := OpenLogFile(path, 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 26, 9, 24, 23, 0, time.Local)
	in := []Entry{
		{Time: at, Level: LevelInfo, Msg: "[zhechev] connected"},
		{Time: at.Add(time.Second), Level: LevelWarn, Msg: "two\nlines"},
		{Time: at.Add(2 * time.Second), Level: LevelError, Msg: "gmail: http 403 PERMISSION_DENIED"},
	}
	for _, e := range in {
		lf.Write(e)
	}
	must(t, lf.Close())

	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Fatalf("file has %d lines, want 3:\n%s", n, data)
	}
	if !strings.HasPrefix(string(data), "2026-11-26 09:24:23 INFO  [zhechev] connected\n") {
		t.Errorf("first line = %q", strings.SplitN(string(data), "\n", 2)[0])
	}
	out := TailEntries(path, 2000)
	if len(out) != 3 {
		t.Fatalf("read back %d entries", len(out))
	}
	for i, e := range out {
		want := in[i]
		want.Msg = strings.ReplaceAll(want.Msg, "\n", "⏎")
		if !e.Time.Equal(want.Time) || e.Level != want.Level || e.Msg != want.Msg {
			t.Errorf("entry %d = %+v, want %+v", i, e, want)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", st.Mode().Perm())
	}
}

// Past the cap the file moves to .1 (replacing an older one) and a new one
// starts.
func TestLogFile_rotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	lf, err := OpenLogFile(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	write := func(n int) {
		for i := range n {
			lf.Write(Entry{Time: time.Now(), Level: LevelInfo, Msg: fmt.Sprint("line number ", i)})
		}
	}
	write(10)
	first, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if st, _ := os.Stat(path); st.Size() >= 200 {
		t.Errorf("current file is %d bytes, want under the cap", st.Size())
	}
	write(10)
	second, _ := os.ReadFile(path + ".1")
	if string(first) == string(second) {
		t.Error("a second rotation did not replace the old .1")
	}
}

func TestTailEntries_lastN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	var b strings.Builder
	for i := range 2500 {
		fmt.Fprintf(&b, "2026-11-26 09:00:00 INFO  line %d\n", i)
	}
	must(t, os.WriteFile(path, []byte(b.String()), 0o600))
	out := TailEntries(path, 2000)
	if len(out) != 2000 || out[0].Msg != "line 500" || out[1999].Msg != "line 2499" {
		t.Errorf("got %d entries, first %q, last %q", len(out), out[0].Msg, out[len(out)-1].Msg)
	}
	if TailEntries(filepath.Join(t.TempDir(), "missing.log"), 10) != nil {
		t.Error("a missing file gave entries")
	}
}

// A line that is not in the log's format (edited by hand, cut short) is
// shown as it is.
func TestParsePlain_garbage(t *testing.T) {
	for _, line := range []string{"not a log line", "2026-11-26 09:00", "2026-13-99 99:99:99 INFO  x"} {
		e := ParsePlain(line)
		if e.Level != LevelInfo || e.Msg != line || !e.Time.IsZero() {
			t.Errorf("ParsePlain(%q) = %+v", line, e)
		}
	}
}

func TestOpenLogFile_unwritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions do not stop this user")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	must(t, os.Mkdir(dir, 0o500))
	if _, err := OpenLogFile(filepath.Join(dir, "m9s.log"), 5<<20); err == nil {
		t.Error("opened a log in a read-only directory")
	}
}

// The sink sees every new entry; loaded entries go first and are not
// written again.
func TestLogger_sinkAndLoad(t *testing.T) {
	l := &Logger{}
	var sunk []string
	l.SetSink(func(e Entry) { sunk = append(sunk, e.Msg) })
	l.Info("now")
	l.Load([]Entry{{Msg: "old 1"}, {Msg: "old 2"}})
	l.Info("later")
	var got []string
	for _, e := range l.Entries() {
		got = append(got, e.Msg)
	}
	if strings.Join(got, ",") != "old 1,old 2,now,later" {
		t.Errorf("entries = %v", got)
	}
	if strings.Join(sunk, ",") != "now,later" {
		t.Errorf("sink got %v", sunk)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A write that fails (disk full, the file gone) is said once, and the
// file is opened again for the next line.
func TestLogFile_writeErrorWarnsOnceAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	lf, err := OpenLogFile(path, 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	var warned []error
	lf.OnError(func(err error) { warned = append(warned, err) })
	lf.f.Close() // the file is pulled from under the log
	lf.Write(Entry{Time: time.Now(), Msg: "lost"})
	lf.Write(Entry{Time: time.Now(), Msg: "kept"})
	lf.f.Close()
	lf.Write(Entry{Time: time.Now(), Msg: "lost again"})
	lf.Write(Entry{Time: time.Now(), Msg: "kept again"})
	if len(warned) != 1 {
		t.Errorf("warned %d times, want once", len(warned))
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "kept\n") || !strings.Contains(string(data), "kept again\n") {
		t.Errorf("file =\n%s", data)
	}
}

// When the old log cannot be moved aside, the file is emptied instead: it
// never grows past the cap.
func TestLogFile_rotateWithoutRenameStaysUnderTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	must(t, os.MkdirAll(filepath.Join(path+".1", "blocker"), 0o700)) // .1 cannot be replaced
	lf, err := OpenLogFile(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	for i := range 50 {
		lf.Write(Entry{Time: time.Now(), Msg: fmt.Sprint("line number ", i)})
	}
	if st, _ := os.Stat(path); st.Size() > 400 {
		t.Errorf("log grew to %d bytes past a 200-byte cap", st.Size())
	}
}

// A hand-edited file: Windows line ends and blank lines read cleanly, and a
// line without a time shows no made-up time.
func TestTailEntries_crlfAndBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	must(t, os.WriteFile(path, []byte("2026-11-26 09:00:00 INFO  a\r\n\r\n2026-11-26 09:00:01 WARN  b\r\nnote\r\n"), 0o600))
	out := TailEntries(path, 10)
	var msgs []string
	for _, e := range out {
		msgs = append(msgs, e.Msg)
	}
	if strings.Join(msgs, "|") != "a|b|note" {
		t.Errorf("entries = %q", msgs)
	}
	skin, _ := LoadSkin("")
	if row := FormatEntry(out[2], skin); strings.Contains(row, "0001-01-01") {
		t.Errorf("a line without a time shows %q", row)
	}
}

// The running m9s reads the lines another process (an m9s command) appends
// to the file — never its own.
func TestLogFile_readsWhatOthersAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	app, err := OpenLogFile(path, 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	cli, err := OpenLogFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.Local)
	app.Write(Entry{Time: at, Msg: "app 1"})
	cli.Write(Entry{Time: at, Msg: "[cli] m9s login work"})
	app.Write(Entry{Time: at, Msg: "app 2"})
	cli.Write(Entry{Time: at, Level: LevelWarn, Msg: "[cli] token: stored"})
	cli.Close()
	var msgs []string
	for _, e := range app.ReadNew() {
		msgs = append(msgs, e.Msg)
	}
	if strings.Join(msgs, "|") != "[cli] m9s login work|[cli] token: stored" {
		t.Errorf("read %q, want only the command's lines", msgs)
	}
	if again := app.ReadNew(); len(again) != 0 {
		t.Errorf("read the same lines twice: %v", again)
	}
}

// A line still being written is read once it ends.
func TestLogFile_partialLineWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	app, err := OpenLogFile(path, 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	defer f.Close()
	f.WriteString("2026-11-26 09:00:00 INFO  half")
	if got := app.ReadNew(); len(got) != 0 {
		t.Fatalf("read a half line: %v", got)
	}
	f.WriteString(" done\n")
	if got := app.ReadNew(); len(got) != 1 || got[0].Msg != "half done" {
		t.Errorf("got %v", got)
	}
}

// A command's log (maxBytes 0) never rotates: only the mail app does, so two
// processes never rotate at once.
func TestLogFile_noCapNeverRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m9s.log")
	lf, err := OpenLogFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		lf.Write(Entry{Time: time.Now(), Msg: fmt.Sprint("line ", i)})
	}
	lf.Close()
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("a command's log rotated")
	}
}

// Entries read from the file are shown (notify) but not written again.
func TestLogger_AddEntries(t *testing.T) {
	l := &Logger{}
	sunk, notified := 0, 0
	l.SetSink(func(Entry) { sunk++ })
	l.OnUpdate(func() { notified++ })
	l.AddEntries([]Entry{{Msg: "a"}, {Msg: "b"}})
	if len(l.Entries()) != 2 || sunk != 0 || notified != 1 {
		t.Errorf("entries %d, sunk %d, notified %d", len(l.Entries()), sunk, notified)
	}
}
