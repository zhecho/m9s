package view

import (
	"strings"
	"testing"

	"github.com/zhecho/m9s/internal/mail"
	"github.com/zhecho/m9s/internal/ui"
)

// rawWithHeaders is a mail as a receiving server leaves it: the newest
// Received on top, authentication results, and a header carrying markup.
const rawWithHeaders = "Received: from mail.example.com (mail.example.com [203.0.113.5])\r\n" +
	"\tby mx.google.com with ESMTPS; Tue, 3 Nov 2026 14:05:01 +0000\r\n" +
	"Authentication-Results: mx.google.com;\r\n" +
	"\tdkim=pass header.i=@example.com header.s=s1;\r\n" +
	"\tspf=pass (google.com: domain of bounce@example.com designates 203.0.113.5) smtp.mailfrom=bounce@example.com;\r\n" +
	"\tdmarc=fail (p=NONE) header.from=example.com\r\n" +
	"Received: from laptop (laptop [10.0.0.2]) by mail.example.com; Tue, 3 Nov 2026 14:05:00 +0000\r\n" +
	"Return-Path: <bounce@example.com>\r\n" +
	"Reply-To: Sales <sales@example.com>\r\n" +
	"Message-ID: <abc@mail.example.com>\r\n" +
	"X-Mailer: Mailer 2.1\r\n" +
	"X-Evil: <script>alert(1)</script>\r\n" +
	"From: Alice <alice@example.com>\r\n" +
	"Subject: Hello\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	"<p>hi</p>\r\n"

func headersPage(t *testing.T, raw string) string {
	t.Helper()
	msg := mail.ParseMessage(browserMsg().Envelope, []byte(raw))
	return browserPage(msg, ui.DefaultBrowserThemes())
}

// panel is the Email headers panel's markup.
func panel(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `<section id="info"`)
	if !ok {
		t.Fatalf("page has no headers panel:\n%s", page)
	}
	p, _, _ := strings.Cut(rest, "</section>")
	return p
}

// The button sits next to Load pictures, and the panel starts closed.
func TestBrowserPage_headersButtonNextToLoadPictures(t *testing.T) {
	page := headersPage(t, rawWithHeaders)
	_, bar, _ := strings.Cut(page, `id="guard"`)
	bar, _, _ = strings.Cut(bar, "</div>")
	if !strings.Contains(bar, `id="allow"`) || !strings.Contains(bar, `aria-controls="info">Email headers</button>`) {
		t.Errorf("guard bar = %s, want Load pictures and Email headers", bar)
	}
	if !strings.Contains(page, `<section id="info" class="info" hidden>`) {
		t.Error("the panel is not closed at first")
	}
}

// Lifting the guard keeps the bar, so Email headers stays.
func TestBrowserPage_headersButtonOutlivesTheGuard(t *testing.T) {
	page := headersPage(t, rawWithHeaders)
	if strings.Contains(page, "bar.remove()") {
		t.Error("Load pictures still removes the whole bar, Email headers with it")
	}
}

// Plain-text mail has no guard; the button gets a bar of its own.
func TestBrowserPage_headersButtonOnPlainMail(t *testing.T) {
	page := headersPage(t, "From: a@example.com\r\nSubject: plain\r\n\r\nhello\r\n")
	if strings.Contains(page, `id="allow"`) || !strings.Contains(page, `aria-controls="info">Email headers</button>`) {
		t.Error("plain mail: want the Email headers button and no Load pictures")
	}
}

// Every header field, exactly as received and in order, as text only.
func TestBrowserPage_panelListsEveryHeaderAsText(t *testing.T) {
	p := panel(t, headersPage(t, rawWithHeaders))
	first := strings.Index(p, "Received: from mail.example.com (mail.example.com [203.0.113.5])\n\tby mx.google.com")
	second := strings.Index(p, "Received: from laptop")
	if first < 0 || second < first {
		t.Errorf("headers not as received, in order:\n%s", p)
	}
	if strings.Contains(p, "<script>") || !strings.Contains(p, "X-Evil: &lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("a header's markup reached the page")
	}
}

// The summary on top: what a reader checks first.
func TestBrowserPage_panelSummary(t *testing.T) {
	p := panel(t, headersPage(t, rawWithHeaders))
	for _, want := range []string{
		"abc@mail.example.com",            // Message-ID
		"bounce@example.com",              // Return-Path
		"Sales &lt;sales@example.com&gt;", // Reply-To
		"Mailer 2.1",                      // sent with
		"SPF pass", "DKIM pass (example.com)", "DMARC fail",
		"2 hops", "laptop", "mail.example.com", // route, oldest first
		"text/html",                    // parts
		humanSize(len(rawWithHeaders)), // size
	} {
		if !strings.Contains(p, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
}

// A message whose content was never downloaded has no headers to show.
func TestBrowserPage_panelWithoutHeaders(t *testing.T) {
	msg := browserMsg()
	msg.TextBody = "hello"
	if p := panel(t, browserPage(msg, ui.DefaultBrowserThemes())); !strings.Contains(p, "not downloaded") {
		t.Errorf("panel = %s, want it to say the headers are not downloaded", p)
	}
}

// The button opens and closes the panel.
func TestBrowserPage_headersButtonToggles(t *testing.T) {
	page := headersPage(t, rawWithHeaders)
	if !strings.Contains(page, "getElementById('info-toggle')") || !strings.Contains(page, "aria-expanded") {
		t.Error("nothing opens the panel")
	}
}
