package sync

import (
	"testing"

	"github.com/zhecho/m9s/internal/mail/gmail"
)

func TestEnvelopeOf_mixedMeansAttachment(t *testing.T) {
	for _, c := range []struct {
		ct   string
		want bool
	}{
		{"multipart/mixed; boundary=x", true},
		{"Multipart/Mixed; boundary=x", true},
		{"multipart/alternative; boundary=x", false},
		{"", false},
	} {
		h := map[string]string{"Subject": "s"}
		if c.ct != "" {
			h["Content-Type"] = c.ct
		}
		if got := envelopeOf(&gmail.MsgMeta{ID: "1", Headers: h}).HasAttach; got != c.want {
			t.Errorf("Content-Type %q: sign %v, want %v", c.ct, got, c.want)
		}
	}
}
