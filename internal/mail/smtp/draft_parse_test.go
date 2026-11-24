package smtp_test

import (
	"strings"
	"testing"

	smtpadapter "github.com/zhecho/m9s/internal/mail/smtp"
)

const bulgarianSubject = "Здравей Яна, как си днес? Това е по-дълъг български предмет за тест"

func parse(t *testing.T, eml string) smtpadapter.Draft {
	t.Helper()
	d, err := smtpadapter.ParseDraft([]byte(eml))
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	return d
}

func TestParseDraft_strayCarriageReturnsAreNotPartOfHeaders(t *testing.T) {
	d := parse(t, "From: me@x.io\r\r\nTo: y@x.io\r\nSubject: test\r\r\n\r\nbody\r\n")
	if d.Subject != "test" || d.From != "me@x.io" {
		t.Errorf("subject %q from %q, want no carriage returns", d.Subject, d.From)
	}
}

func TestParseDraft_separatorLineWithStrayCR(t *testing.T) {
	d := parse(t, "Subject: hi\r\n\r\r\nthe body\r\n")
	if d.Subject != "hi" || strings.TrimSpace(d.Body) != "the body" {
		t.Errorf("subject %q body %q: a blank line with a stray CR still ends the headers", d.Subject, d.Body)
	}
}

func TestParseDraft_foldedSubjectIsJoined(t *testing.T) {
	// vim (filetype=mail, textwidth=72) wraps a long subject like this.
	d := parse(t, "To: y@x.io\nSubject: Здравей Яна, как си днес? Това е по-дълъг\n български предмет за тест\n\nbody\n")
	if d.Subject != bulgarianSubject {
		t.Errorf("subject = %q, want the wrapped line joined back", d.Subject)
	}
}

func TestParseDraft_encodedWordSubjectIsDecoded(t *testing.T) {
	d := parse(t, "Subject: =?utf-8?q?=D0=97=D0=B4=D1=80=D0=B0=D0=B2=D0=B5=D0=B9?= =?utf-8?b?0K/QvdCw?=\n\nbody\n")
	if d.Subject != "ЗдравейЯна" {
		t.Errorf("subject = %q, want the encoded words decoded", d.Subject)
	}
}

func TestParseDraft_bodyLinesLoseStrayCR(t *testing.T) {
	d := parse(t, "Subject: s\n\nред едно\r\r\nред две\r\n")
	if strings.Contains(d.Body, "\r") || !strings.Contains(d.Body, "ред едно\nред две") {
		t.Errorf("body = %q", d.Body)
	}
}

func TestFormatDraft_headerValuesStayOnOneLine(t *testing.T) {
	eml, err := smtpadapter.FormatDraft(smtpadapter.Draft{Subject: "a\r\nBcc: evil@x.io", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(eml), "\nBcc:") || strings.Contains(string(eml), "\r\r") {
		t.Errorf("a newline in the subject started a new header:\n%q", eml)
	}
}

// TestDraftRoundTrip_survivesAnEditorThatAddsCR simulates an editor that
// writes CRLF again on top of CRLF, five edits in a row.
func TestDraftRoundTrip_survivesAnEditorThatAddsCR(t *testing.T) {
	d := smtpadapter.Draft{From: "me@x.io", To: []string{"y@x.io"}, Subject: bulgarianSubject, Body: "Тяло на писмото\nвтори ред"}
	for i := 0; i < 5; i++ {
		eml, err := smtpadapter.FormatDraft(d)
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.ReplaceAll(string(eml), "\n", "\r\n") // "\r\n" becomes "\r\r\n"
		d = parse(t, edited)
	}
	if d.Subject != bulgarianSubject {
		t.Errorf("subject after 5 edits = %q", d.Subject)
	}
	if strings.TrimSpace(d.Body) != "Тяло на писмото\nвтори ред" {
		t.Errorf("body after 5 edits = %q", d.Body)
	}
}

func TestFormatDraftForUpload_encodesNonASCIIHeaders(t *testing.T) {
	d := smtpadapter.Draft{From: "Митко <me@x.io>", To: []string{"Яна <y@x.io>"}, Subject: bulgarianSubject, Body: "Тяло"}
	eml, err := smtpadapter.FormatDraftForUpload(d)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.SplitN(string(eml), "\r\n\r\n", 2)[0]
	for _, line := range strings.Split(head, "\r\n") {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("raw non-ASCII in an uploaded header line: %q", line)
			}
		}
	}
	if !strings.Contains(head, "charset=utf-8") {
		t.Errorf("uploaded draft must declare its charset:\n%s", head)
	}
	back := parse(t, string(eml))
	if back.Subject != bulgarianSubject || !strings.Contains(back.Body, "Тяло") {
		t.Errorf("upload format does not read back: subject %q body %q", back.Subject, back.Body)
	}
}
