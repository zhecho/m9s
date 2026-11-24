package view

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
	msync "github.com/zhecho/m9s/internal/sync"
)

const shortReasonMaxRunes = 30

// shortReason reduces a connection error to a few words for the header's
// Status row. The full error goes to the bottom row and the log.
func shortReason(err error) string {
	if err == nil {
		return "unknown error"
	}
	if errors.Is(err, imapadapter.ErrAuth) {
		return "auth failed"
	}
	if errors.Is(err, msync.ErrNoPassword) {
		return "no password"
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, rule := range []struct{ needle, reason string }{
		{"no such host", "no such host"},
		{"connection refused", "connection refused"},
		{"i/o timeout", "timeout"},
		{"deadline exceeded", "timeout"},
		{"network is unreachable", "network unreachable"},
		{"certificate", "TLS certificate error"},
		{"connection lost", "connection lost"},
		{"eof", "connection lost"},
		{"closed", "connection lost"},
	} {
		if strings.Contains(lower, rule.needle) {
			return rule.reason
		}
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	if r := []rune(msg); len(r) > shortReasonMaxRunes {
		msg = string(r[:shortReasonMaxRunes])
	}
	return msg
}

// statusLine renders an account's connection state as the header's Status
// text and the skin role to colour it with. known is false when no status
// event has arrived for the account yet.
func statusLine(st msync.AccountStatus, known bool) (text, role string) {
	if !known {
		return "… connecting", "dim"
	}
	switch st.State {
	case msync.StateConnected:
		return "● connected", "flash_ok"
	case msync.StateFailed:
		text = "✗ " + shortReason(st.Err)
		if st.RetryIn > 0 {
			// The delay as reported when the attempt failed, not a live countdown.
			text += fmt.Sprintf(" · retry in %ds", int(math.Ceil(st.RetryIn.Seconds())))
		}
		return text, "flash_err"
	default:
		return "… connecting", "dim"
	}
}

// retryInText is used in log lines.
func retryInText(d time.Duration) string {
	if d <= 0 {
		return "no automatic retry"
	}
	return "retry in " + d.Round(time.Second).String()
}
