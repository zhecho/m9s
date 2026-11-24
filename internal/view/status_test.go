package view

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	msync "github.com/zhecho/m9s/internal/sync"
)

func TestShortReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dns", fmt.Errorf("imap dial h:993: %w", errors.New("dial tcp: lookup h: no such host")), "no such host"},
		{"refused", fmt.Errorf("imap dial h:993: %w", errors.New("dial tcp 1.2.3.4:993: connect: connection refused")), "connection refused"},
		{"timeout", errors.New("dial tcp 1.2.3.4:993: i/o timeout"), "timeout"},
		{"deadline", errors.New("context deadline exceeded"), "timeout"},
		{"unreachable", errors.New("dial tcp 1.2.3.4:993: connect: network is unreachable"), "network unreachable"},
		{"auth", fmt.Errorf("%w: NO [AUTHENTICATIONFAILED] bad", imapadapter.ErrAuth), "auth failed"},
		{"no password", fmt.Errorf("%w: no imap password for \"a\"", msync.ErrNoPassword), "no password"},
		{"tls", errors.New("tls: failed to verify certificate: x509: certificate has expired"), "TLS certificate error"},
		{"lost", errors.New("idle connection lost"), "connection lost"},
		{"eof", errors.New("imap LIST: unexpected EOF"), "connection lost"},
		{"closed", errors.New("use of closed network connection"), "connection lost"},
		{"unknown takes last segment", errors.New("imap LIST: server said no"), "server said no"},
		{"nil", nil, "unknown error"},
	}
	for _, tt := range tests {
		if got := shortReason(tt.err); got != tt.want {
			t.Errorf("%s: shortReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestShortReason_truncatesLongUnknown(t *testing.T) {
	got := shortReason(errors.New(strings.Repeat("ж", 60)))
	if n := utf8.RuneCountInString(got); n != 30 {
		t.Errorf("unknown reason is %d runes, want it cut to 30: %q", n, got)
	}
}

func TestStatusLine(t *testing.T) {
	refused := errors.New("dial tcp: connect: connection refused")
	tests := []struct {
		name     string
		st       msync.AccountStatus
		known    bool
		wantText string
		wantRole string
	}{
		{"unknown", msync.AccountStatus{}, false, "… connecting", "dim"},
		{"connecting", msync.AccountStatus{State: msync.StateConnecting}, true, "… connecting", "dim"},
		{"connected", msync.AccountStatus{State: msync.StateConnected}, true, "● connected", "flash_ok"},
		{"failed with retry", msync.AccountStatus{State: msync.StateFailed, Err: refused, RetryIn: 8 * time.Second}, true,
			"✗ connection refused · retry in 8s", "flash_err"},
		{"failed sub-second retry rounds up", msync.AccountStatus{State: msync.StateFailed, Err: refused, RetryIn: 300 * time.Millisecond}, true,
			"✗ connection refused · retry in 1s", "flash_err"},
		{"failed no retry", msync.AccountStatus{State: msync.StateFailed, Err: fmt.Errorf("%w: x", imapadapter.ErrAuth)}, true,
			"✗ auth failed", "flash_err"},
	}
	for _, tt := range tests {
		text, role := statusLine(tt.st, tt.known)
		if text != tt.wantText || role != tt.wantRole {
			t.Errorf("%s: statusLine = (%q, %q), want (%q, %q)", tt.name, text, role, tt.wantText, tt.wantRole)
		}
	}
}
