package imap

import (
	"errors"
	"io"
	"testing"

	imap "github.com/emersion/go-imap/v2"
)

func TestLoginError_onlyCredentialRejectionIsAuth(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantAuth bool
	}{
		{"NO AUTHENTICATIONFAILED", &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "bad"}, true},
		{"plain NO", &imap.Error{Type: imap.StatusResponseTypeNo, Text: "LOGIN failed"}, true},
		{"NO UNAVAILABLE is transient", &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeUnavailable, Text: "try later"}, false},
		{"BAD is not a credential problem", &imap.Error{Type: imap.StatusResponseTypeBad, Text: "syntax"}, false},
		{"BYE (connection limit)", &imap.Error{Type: imap.StatusResponseTypeBye, Text: "too many connections"}, false},
		{"socket dropped mid-login", io.ErrUnexpectedEOF, false},
	}
	for _, tt := range tests {
		got := loginError(tt.err)
		if errors.Is(got, ErrAuth) != tt.wantAuth {
			t.Errorf("%s: errors.Is(loginError, ErrAuth) = %v, want %v (err %v)", tt.name, !tt.wantAuth, tt.wantAuth, got)
		}
		if !errors.Is(got, tt.err) {
			t.Errorf("%s: the cause must stay inspectable, got %v", tt.name, got)
		}
	}
}
