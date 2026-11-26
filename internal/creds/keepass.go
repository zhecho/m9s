package creds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhecho/m9s/internal/config"
	"github.com/zhecho/m9s/internal/keepassxc"
)

// kpClient is the part of *keepassxc.Client a Session uses.
type kpClient interface {
	IsOpen(ctx context.Context, triggerUnlock bool) error
	WaitUnlocked(ctx context.Context) error
	Associate(ctx context.Context) (keepassxc.Association, error)
	TestAssociate(ctx context.Context, a keepassxc.Association) error
	Logins(ctx context.Context, url string, a keepassxc.Association) ([]keepassxc.Login, error)
	SetLogin(ctx context.Context, url, login, password, uuid string, a keepassxc.Association) error
	Groups(ctx context.Context) ([]keepassxc.Group, error)
	Close() error
}

// AssocStore remembers the approval the user gave in KeePassXC.
type AssocStore struct {
	Load   func() (id, key string, err error)
	Save   func(id, key string) error
	Delete func() error
}

const (
	noticeLocked  = "KeePassXC is locked — unlock it in the KeePassXC window…"
	noticeApprove = "Approve the connection in KeePassXC and name it m9s…"
	// noticeSlow is filled with what is asked for: "the entry for <user> on
	// https://<host>".
	noticeSlow = "Waiting for KeePassXC — allow m9s access to %s if it asks…"
)

// Session is one start's conversation with KeePassXC: it connects once,
// waits for one unlock and one approval if they are needed, asks once per
// mail server, and picks each account's entry by user name.
//
// Everything that can keep the user waiting shares one budget (Timeout) and
// can be ended with Skip. Once KeePassXC as a whole has failed — not
// running, not unlocked, not approved, connection lost — every later
// question gets that same answer at once.
type Session struct {
	Dial     func(ctx context.Context) (kpClient, error)
	Assoc    AssocStore
	Timeout  time.Duration
	OnNotice func(string)
	// NoticeDelay is how long an ordinary request may take before the user
	// is told that m9s is waiting for KeePassXC (which may be asking them
	// to allow access to an entry). Quick answers show nothing.
	NoticeDelay time.Duration

	mu        sync.Mutex // one question at a time
	started   bool
	client    kpClient
	assoc     keepassxc.Association
	failed    error
	remaining time.Duration
	hosts     map[string][]keepassxc.Login
	about     string // the entry being asked for, as the waiting notice names it
	quiet     bool   // a quiet session: Where asks KeePassXC for no entries
	groups    []keepassxc.Group
	groupsSet bool // groups was asked for (an answer, or none to be had)

	waited   atomic.Bool
	skipOnce sync.Once
	skip     chan struct{} // closed by Skip
	waiting  atomic.Bool   // a wait is in progress: Skip applies
	initOnce sync.Once
}

// NewSession is a Session against the real KeePassXC, remembering the
// approval in the OS keychain and waiting two minutes at most.
func NewSession(onNotice func(string)) *Session {
	return &Session{
		Dial: func(ctx context.Context) (kpClient, error) {
			path, err := keepassxc.SocketPath()
			if err != nil {
				return nil, err
			}
			return keepassxc.Dial(ctx, path)
		},
		Assoc: AssocStore{
			Load:   config.KeePassXCAssociation,
			Save:   config.SetKeePassXCAssociation,
			Delete: config.DeleteKeePassXCAssociation,
		},
		Timeout:     2 * time.Minute,
		NoticeDelay: time.Second,
		OnNotice:    onNotice,
	}
}

func (s *Session) init() {
	s.initOnce.Do(func() { s.skip = make(chan struct{}) })
}

// Skip stops waiting for KeePassXC: the wait in progress fails, and with it
// KeePassXC for the rest of this start. Without a wait in progress it does
// nothing.
func (s *Session) Skip() {
	s.init()
	if s.waiting.Load() {
		s.skipOnce.Do(func() { close(s.skip) })
	}
}

// Waited reports whether the user was ever kept waiting.
func (s *Session) Waited() bool { return s.waited.Load() }

// Close ends the connection, if there is one.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
}

// NoEntryError is KeePassXC having no entry for User on Host.
type NoEntryError struct{ Host, User string }

func (e *NoEntryError) Error() string {
	return fmt.Sprintf("no entry for %s on %s in KeePassXC — set the entry's URL field to https://%s and its user name to %s",
		e.User, e.Host, e.Host, e.User)
}

// Password returns the password of the entry whose URL is host and whose
// user name is user.
func (s *Session) Password(ctx context.Context, host, user string) (string, error) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asking(host, user)

	l, err := s.entry(ctx, host, user)
	if err != nil {
		return "", err
	}
	return l.Password, nil
}

// SaveLogin stores password as the entry for user on host: it updates the
// one entry there with that user name, or creates one when there is none.
// The entry's URL is https://<host>. The password is never part of an error.
func (s *Session) SaveLogin(ctx context.Context, host, user, password string) error {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asking(host, user)

	uuid := ""
	l, err := s.entry(ctx, host, user)
	var none *NoEntryError
	switch {
	case err == nil:
		uuid = l.UUID
	case errors.As(err, &none):
	default:
		return err
	}
	err = s.ask(ctx, func(actx context.Context) error {
		return s.client.SetLogin(actx, "https://"+host, user, password, uuid, s.assoc)
	})
	delete(s.hosts, host) // read again next time, with the new entry
	if err != nil {
		return fmt.Errorf("KeePassXC did not store the entry for %s on %s: %w", user, host, s.whole(ctx, err))
	}
	return nil
}

// ErrNotApproved means KeePassXC has not approved m9s (or no longer
// accepts the approval it gave); a quiet save does not ask for one.
var ErrNotApproved = errors.New("m9s is not approved in KeePassXC: start m9s with KeePassXC unlocked to approve it")

// SaveLoginQuiet is SaveLogin for a background save that must never
// interrupt the owner: it never asks KeePassXC to show its unlock window
// and never asks for an approval. A locked database is keepassxc.ErrLocked,
// KeePassXC not running keepassxc.ErrNotRunning, no (accepted) approval
// ErrNotApproved — the caller tries again later. A Session used for this
// should be used for nothing else.
func (s *Session) SaveLoginQuiet(ctx context.Context, host, user, password string) error {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asking(host, user)
	s.quiet = true
	if !s.started {
		s.started = true
		s.remaining = s.Timeout
		s.hosts = map[string][]keepassxc.Login{}
		s.failed = s.connectQuiet(ctx)
	}
	if s.failed != nil {
		return s.failed
	}
	var logins []keepassxc.Login
	err := s.ask(ctx, func(actx context.Context) error {
		var err error
		logins, err = s.client.Logins(actx, "https://"+host, s.assoc)
		return err
	})
	switch {
	case errors.Is(err, keepassxc.ErrNoLogins):
	case errors.Is(err, keepassxc.ErrLocked):
		return keepassxc.ErrLocked
	case err != nil:
		return s.whole(ctx, err)
	}
	s.hosts[host] = logins // for Where; an entry created below is not in it
	uuid := ""
	l, err := pick(logins, host, user)
	var none *NoEntryError
	switch {
	case err == nil:
		uuid = l.UUID
	case errors.As(err, &none):
	default:
		return err
	}
	err = s.ask(ctx, func(actx context.Context) error {
		return s.client.SetLogin(actx, "https://"+host, user, password, uuid, s.assoc)
	})
	if errors.Is(err, keepassxc.ErrLocked) {
		return keepassxc.ErrLocked
	}
	if err != nil {
		return fmt.Errorf("KeePassXC did not store the entry for %s on %s: %w", user, host, s.whole(ctx, err))
	}
	return nil
}

// connectQuiet dials and checks, without asking anything of the owner,
// that the database is open and the saved approval accepted.
func (s *Session) connectQuiet(ctx context.Context) error {
	var client kpClient
	err := s.ask(ctx, func(actx context.Context) error {
		var err error
		client, err = s.Dial(actx)
		return err
	})
	switch {
	case err == nil:
	case errors.Is(err, keepassxc.ErrUnavailable):
		return keepassxc.ErrUnavailable
	case errors.Is(err, keepassxc.ErrNotRunning):
		return keepassxc.ErrNotRunning
	case ctx.Err() != nil:
		return errCancelled
	default:
		return err
	}
	s.client = client
	if err := s.ask(ctx, func(actx context.Context) error { return s.client.IsOpen(actx, false) }); err != nil {
		if errors.Is(err, keepassxc.ErrLocked) {
			return keepassxc.ErrLocked
		}
		return s.whole(ctx, err)
	}
	id, key, err := s.Assoc.Load()
	if err != nil {
		return ErrNotApproved
	}
	a := keepassxc.Association{ID: id, Key: key}
	err = s.ask(ctx, func(actx context.Context) error { return s.client.TestAssociate(actx, a) })
	var refused *keepassxc.ProtocolError
	switch {
	case errors.As(err, &refused):
		return ErrNotApproved
	case err != nil:
		return s.whole(ctx, err)
	}
	s.assoc = a
	return nil
}

// entry returns the one entry for user on host, connecting first if this
// is the first question. The caller holds s.mu.
func (s *Session) entry(ctx context.Context, host, user string) (keepassxc.Login, error) {
	if !s.started {
		s.started = true
		s.remaining = s.Timeout
		s.hosts = map[string][]keepassxc.Login{}
		s.failed = s.connect(ctx)
	}
	if s.failed != nil {
		return keepassxc.Login{}, s.failed
	}

	logins, known := s.hosts[host]
	if !known {
		var err error
		logins, err = s.logins(ctx, host)
		if err != nil {
			return keepassxc.Login{}, err
		}
		s.hosts[host] = logins
	}

	return pick(logins, host, user)
}

// pick returns the one login of logins (the entries on host) for user.
func pick(logins []keepassxc.Login, host, user string) (keepassxc.Login, error) {
	var match []keepassxc.Login
	for _, l := range logins {
		if strings.EqualFold(strings.TrimSpace(l.User), strings.TrimSpace(user)) {
			match = append(match, l)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return keepassxc.Login{}, &NoEntryError{Host: host, User: user}
	default:
		return keepassxc.Login{}, fmt.Errorf("%d entries for %s on %s in KeePassXC — remove or rename one", len(match), user, host)
	}
}

// connect dials, gets the database unlocked and the connection approved.
// What it returns fails KeePassXC for the whole start.
func (s *Session) connect(ctx context.Context) error {
	var client kpClient
	err := s.ask(ctx, func(actx context.Context) error {
		var err error
		client, err = s.Dial(actx)
		return err
	})
	if err != nil {
		if isGiveUp(err) {
			return err
		}
		switch {
		case errors.Is(err, keepassxc.ErrUnavailable):
			return keepassxc.ErrUnavailable
		case errors.Is(err, keepassxc.ErrNotRunning):
			return keepassxc.ErrNotRunning
		case ctx.Err() != nil:
			return errCancelled
		}
		return err
	}
	s.client = client
	if err := s.unlock(ctx); err != nil {
		return err
	}
	return s.approve(ctx)
}

var (
	errCancelled = errors.New("cancelled")
	errClosed    = errors.New("KeePassXC closed the connection")
)

// unlock returns once the database is open, asking KeePassXC to show its
// unlock window and waiting if it is not.
func (s *Session) unlock(ctx context.Context) error {
	for {
		err := s.ask(ctx, func(actx context.Context) error { return s.client.IsOpen(actx, true) })
		if err == nil {
			return nil
		}
		if !errors.Is(err, keepassxc.ErrLocked) {
			return s.whole(ctx, err)
		}
		err = s.wait(ctx, noticeLocked, "unlock", "skipped — KeePassXC was not unlocked",
			func(wctx context.Context) error { return s.client.WaitUnlocked(wctx) })
		if err != nil {
			return err
		}
	}
}

// approve makes sure KeePassXC accepts this program, asking the user to
// approve it the first time, or again if the remembered approval is refused.
func (s *Session) approve(ctx context.Context) error {
	if id, key, err := s.Assoc.Load(); err == nil {
		a := keepassxc.Association{ID: id, Key: key}
		err := s.ask(ctx, func(actx context.Context) error { return s.client.TestAssociate(actx, a) })
		if err == nil {
			s.assoc = a
			return nil
		}
		var refused *keepassxc.ProtocolError
		if !errors.As(err, &refused) {
			return s.whole(ctx, err)
		}
		_ = s.Assoc.Delete()
	}
	return s.wait(ctx, noticeApprove, "approval", "skipped — the connection was not approved in KeePassXC",
		func(wctx context.Context) error {
			a, err := s.client.Associate(wctx)
			if err != nil {
				return err
			}
			s.assoc = a
			// Not being able to remember it only means asking again next time.
			_ = s.Assoc.Save(a.ID, a.Key)
			return nil
		})
}

// logins asks KeePassXC for the entries on host. A database locked again in
// the meantime is waited for like the first time.
func (s *Session) logins(ctx context.Context, host string) ([]keepassxc.Login, error) {
	for {
		var logins []keepassxc.Login
		err := s.ask(ctx, func(actx context.Context) error {
			var err error
			logins, err = s.client.Logins(actx, "https://"+host, s.assoc)
			return err
		})
		switch {
		case err == nil:
			return logins, nil
		case errors.Is(err, keepassxc.ErrNoLogins):
			return nil, nil
		case errors.Is(err, keepassxc.ErrLocked):
			if err := s.unlock(ctx); err != nil {
				s.failed = err
				return nil, err
			}
		default:
			whole := s.whole(ctx, err)
			if errors.Is(whole, errClosed) || errors.Is(whole, errCancelled) || isGiveUp(whole) {
				s.failed = whole
			}
			return nil, whole
		}
	}
}

// giveUp is a wait that ended without an answer: the budget ran out or the
// user pressed Esc. It fails KeePassXC for the rest of the start.
type giveUp struct{ msg string }

func (g *giveUp) Error() string { return g.msg }

func isGiveUp(err error) bool {
	var g *giveUp
	return errors.As(err, &g)
}

// ask runs one ordinary request under the shared budget and Skip. It is
// quick unless KeePassXC is showing the user a dialog; if it takes longer
// than NoticeDelay, the user is told what m9s is waiting for.
func (s *Session) ask(ctx context.Context, f func(context.Context) error) error {
	about := s.about
	if about == "" {
		about = "the entry"
	}
	return s.waitAfter(ctx, s.NoticeDelay, fmt.Sprintf(noticeSlow, about), "did not answer", "skipped — KeePassXC did not answer", f)
}

// wait runs f, which waits on the user, under the shared budget and Skip,
// showing notice meanwhile. what names the wait in the time-out reason.
func (s *Session) wait(ctx context.Context, notice, what, skipped string, f func(context.Context) error) error {
	return s.waitAfter(ctx, 0, notice, what+" was not completed", skipped, f)
}

// waitAfter runs f under the shared budget and Skip. notice is shown once f
// has taken delay (at once for 0). ended words a time-out ("KeePassXC
// <ended> (waited 2 min)"), skipped an Esc.
func (s *Session) waitAfter(ctx context.Context, delay time.Duration, notice, ended, skipped string, f func(context.Context) error) error {
	var shown atomic.Bool
	show := func() {
		shown.Store(true)
		s.waited.Store(true)
		s.notify(notice)
	}
	if delay <= 0 {
		show()
	} else {
		t := time.AfterFunc(delay, show)
		defer t.Stop()
	}
	defer func() {
		if shown.Load() {
			s.notify("")
		}
	}()

	wctx, cancel := context.WithTimeout(ctx, s.remaining)
	defer cancel()
	s.waiting.Store(true)
	stop := make(chan struct{})
	go func() {
		select {
		case <-s.skip:
			cancel()
		case <-stop:
		}
	}()
	start := time.Now()
	err := f(wctx)
	close(stop)
	s.waiting.Store(false)
	s.remaining -= time.Since(start)
	if s.remaining < 0 {
		s.remaining = 0
	}

	if err == nil {
		return nil
	}
	select {
	case <-s.skip:
		return &giveUp{skipped}
	default:
	}
	switch {
	case ctx.Err() != nil:
		return errCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return &giveUp{fmt.Sprintf("KeePassXC %s (waited %s)", ended, waitedText(s.Timeout))}
	}
	return err
}

// whole words an error that concerns KeePassXC as a whole.
func (s *Session) whole(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, keepassxc.ErrClosed):
		return errClosed
	case ctx.Err() != nil:
		return errCancelled
	}
	return err
}

func (s *Session) notify(text string) {
	if s.OnNotice != nil {
		s.OnNotice(text)
	}
}

// waitedText is a wait budget as the user reads it: "2 min", "50ms".
func waitedText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}

// asking notes which entry the next requests are for, for the waiting
// notice. The caller holds s.mu.
func (s *Session) asking(host, user string) {
	s.about = fmt.Sprintf("the entry for %s on https://%s", user, host)
}

// Where names the entry for user on host by its place in the database: its
// groups from the root, then its title ("Root/Email/Work/graph"). KeePassXC
// tells only the name of an entry's own group, so the path comes from the
// group tree, asked for once; a group whose name is not unique, or no tree,
// gives just that name. An entry it cannot find is "https://<host> /
// <user>". It is meant for after Password or a save: it reuses what they
// read, and a quiet session asks KeePassXC for no entries.
func (s *Session) Where(ctx context.Context, host, user string) string {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	fallback := fmt.Sprintf("https://%s / %s", host, user)
	if s.client == nil || s.failed != nil {
		return fallback
	}
	logins, known := s.hosts[host]
	if !known && !s.quiet {
		err := s.ask(ctx, func(actx context.Context) error {
			var err error
			logins, err = s.client.Logins(actx, "https://"+host, s.assoc)
			return err
		})
		if err != nil && !errors.Is(err, keepassxc.ErrNoLogins) {
			return fallback
		}
		s.hosts[host] = logins
	}
	l, err := pick(logins, host, user)
	if err != nil {
		return fallback
	}
	if !s.groupsSet {
		s.groupsSet = true
		_ = s.ask(ctx, func(actx context.Context) error {
			var err error
			s.groups, err = s.client.Groups(actx)
			return err
		})
	}
	title := l.Name
	if title == "" {
		title = "https://" + host
	}
	if g := groupPath(s.groups, l.Group); g != "" {
		return g + "/" + title
	}
	return title
}

// groupPath is the path from the root to the one group called name; name
// alone when there is no such group or more than one.
func groupPath(groups []keepassxc.Group, name string) string {
	var found []string
	var walk func(gs []keepassxc.Group, parent string)
	walk = func(gs []keepassxc.Group, parent string) {
		for _, g := range gs {
			p := g.Name
			if parent != "" {
				p = parent + "/" + g.Name
			}
			if g.Name == name {
				found = append(found, p)
			}
			walk(g.Children, p)
		}
	}
	if name != "" {
		walk(groups, "")
	}
	if len(found) == 1 {
		return found[0]
	}
	return name
}
