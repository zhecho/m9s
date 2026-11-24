package sync

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestServerIDText(t *testing.T) {
	for _, tc := range []struct {
		id   *imap.IDData
		want string
	}{
		{&imap.IDData{Name: "Dovecot", Version: "2.3.21"}, "Dovecot / 2.3.21"},
		{&imap.IDData{Name: "GImap", Vendor: "Google, Inc."}, "GImap / -"},
		{&imap.IDData{Vendor: "Fastmail"}, "Fastmail / -"},
		{&imap.IDData{}, ""},
		{nil, ""},
	} {
		if got := serverIDText(tc.id); got != tc.want {
			t.Errorf("serverIDText(%+v) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
