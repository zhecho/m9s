package view

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhecho/m9s/internal/mail"
)

func browserMsg() *mail.Message {
	return &mail.Message{
		Envelope: &mail.Envelope{
			UID:     7,
			Subject: "Оферта <нова>",
			From:    []mail.Address{{Name: "Alice", Email: "alice@example.com"}},
			To:      []mail.Address{{Email: "me@x.io"}},
			CC:      []mail.Address{{Email: "boss@x.io"}},
			Date:    time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC),
		},
	}
}

// frameContent returns what the page shows inside the mail's own frame.
func frameContent(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `srcdoc="`)
	if !ok {
		t.Fatalf("page has no framed mail body:\n%s", page)
	}
	doc, _, _ := strings.Cut(rest, `"`)
	r := strings.NewReplacer("&#34;", `"`, "&quot;", `"`, "&lt;", "<", "&gt;", ">", "&#39;", "'", "&amp;", "&")
	return r.Replace(doc)
}

func TestBrowserPage_showsWhoWhenAndSubject(t *testing.T) {
	page := browserPage(browserMsg())
	for _, want := range []string{
		"Alice &lt;alice@example.com&gt;", "me@x.io", "boss@x.io",
		"Оферта &lt;нова&gt;", "Tue, 03 Nov 2026 14:05", `<meta charset="utf-8">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
}

func TestBrowserPage_htmlMailIsShownAsDesigned(t *testing.T) {
	msg := browserMsg()
	msg.TextBody = "plain fallback"
	msg.HTMLBody = `<html><body><h1 style="color:red">Sale</h1><img src="https://shop.example/banner.png"></body></html>`
	body := frameContent(t, browserPage(msg))
	if !strings.Contains(body, `<h1 style="color:red">Sale</h1>`) || !strings.Contains(body, "https://shop.example/banner.png") {
		t.Errorf("framed mail = %q, want the sender's HTML untouched", body)
	}
	if strings.Contains(body, "plain fallback") {
		t.Error("the plain-text alternative was shown although the mail has HTML")
	}
}

func TestBrowserPage_embeddedPicturesAreInlined(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	msg := browserMsg()
	msg.HTMLBody = `<p><img src="cid:logo@m9s"> <img src='CID:logo@m9s'></p>`
	msg.Attachments = []*mail.Attachment{{Filename: "logo.png", ContentType: "image/png", ContentID: "logo@m9s", Inline: true, Data: png}}
	body := frameContent(t, browserPage(msg))
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if strings.Count(body, uri) != 2 || strings.Contains(strings.ToLower(body), "cid:") {
		t.Errorf("framed mail = %q, want both cid references replaced by %s", body, uri)
	}
}

func TestBrowserPage_mailScriptsCannotRun(t *testing.T) {
	msg := browserMsg()
	msg.HTMLBody = `<script>alert(1)</script><p>hi</p>`
	page := browserPage(msg)
	_, after, _ := strings.Cut(page, "<iframe")
	tag, _, _ := strings.Cut(after, "srcdoc=")
	if !strings.Contains(tag, "sandbox=") || strings.Contains(tag, "allow-scripts") {
		t.Errorf("the mail's frame must be sandboxed without scripts, got <iframe%s", tag)
	}
	if strings.Contains(strings.Replace(page, "srcdoc=", "", 1), "<script>alert(1)") {
		t.Error("the mail's script reached the page outside the sandboxed frame")
	}
}

func TestBrowserPage_plainTextMailIsEscapedAndReadable(t *testing.T) {
	msg := browserMsg()
	msg.TextBody = "Здравей,\n1 < 2 & <b>not bold</b>"
	page := browserPage(msg)
	if !strings.Contains(page, "Здравей,\n1 &lt; 2 &amp; &lt;b&gt;not bold&lt;/b&gt;") {
		t.Errorf("plain text must be shown literally:\n%s", page)
	}
	if strings.Contains(page, "<iframe") {
		t.Error("a plain-text mail needs no frame")
	}
}

func TestBrowserPage_pictureAttachmentsShownOthersListed(t *testing.T) {
	jpg := []byte{0xff, 0xd8, 0xff}
	msg := browserMsg()
	msg.TextBody = "see attached"
	msg.Attachments = []*mail.Attachment{
		{Filename: "photo.jpg", ContentType: "image/jpeg", Data: jpg},
		{Filename: "bill <2>.pdf", ContentType: "application/pdf", Data: []byte("%PDF")},
	}
	page := browserPage(msg)
	if !strings.Contains(page, `src="data:image/jpeg;base64,`+base64.StdEncoding.EncodeToString(jpg)+`"`) {
		t.Errorf("the attached photo is not shown:\n%s", page)
	}
	if !strings.Contains(page, "bill &lt;2&gt;.pdf") {
		t.Errorf("the pdf is not listed by name:\n%s", page)
	}
	if strings.Contains(page, base64.StdEncoding.EncodeToString([]byte("%PDF"))) {
		t.Error("a non-picture attachment must only be listed, not embedded")
	}
}

func TestBrowserPage_embeddedPictureUsedByTheHTMLIsNotRepeatedBelow(t *testing.T) {
	msg := browserMsg()
	msg.HTMLBody = `<img src="cid:logo">`
	msg.Attachments = []*mail.Attachment{{Filename: "logo.png", ContentType: "image/png", ContentID: "logo", Inline: true, Data: []byte("x")}}
	page := browserPage(msg)
	if strings.Contains(page, "logo.png") {
		t.Errorf("a picture the mail already shows was listed again:\n%s", page)
	}
}

func TestBrowserFiles_writePrivatePageAndCleanUp(t *testing.T) {
	bf := &browserFiles{base: t.TempDir()}
	msg := browserMsg()
	msg.TextBody = "hello"
	path, err := bf.write(msg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "hello") {
		t.Fatalf("page at %s: %v %q", path, err, data)
	}
	if filepath.Ext(path) != ".html" {
		t.Errorf("page %s must end in .html so the browser renders it", path)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("page mode = %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want 0700", st.Mode().Perm())
	}
	second, err := bf.write(msg)
	if err != nil || filepath.Dir(second) != filepath.Dir(path) {
		t.Errorf("second page %s (%v) must land in the same folder as %s", second, err, path)
	}
	bf.cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("folder still there after cleanup: %v", err)
	}
	bf.cleanup() // nothing left: must not fail
}

func TestBrowserPage_hostileContentTypeCannotBreakOutOfThePage(t *testing.T) {
	evil := `image/png" onerror="alert(1)`
	msg := browserMsg()
	msg.HTMLBody = `<img src="cid:x">`
	msg.Attachments = []*mail.Attachment{
		{Filename: "a.png", ContentType: evil, Data: []byte("x")},
		{Filename: "b.png", ContentType: evil, ContentID: "x", Inline: true, Data: []byte("x")},
	}
	page := browserPage(msg)
	if strings.Contains(page, `onerror="alert(1)`) || strings.Contains(frameContent(t, page), "onerror") {
		t.Errorf("the mail's content type was written into the page as markup:\n%s", page)
	}
	if !strings.Contains(page, "a.png") {
		t.Error("an attachment that is not a known picture type must still be listed by name")
	}
}

func TestBrowserPage_onlyKnownPictureTypesAreEmbedded(t *testing.T) {
	msg := browserMsg()
	msg.TextBody = "x"
	msg.Attachments = []*mail.Attachment{
		{Filename: "ok.jpg", ContentType: "IMAGE/JPEG; name=ok.jpg", Data: []byte("j")},
		{Filename: "drawing.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")},
		{Filename: "page.html", ContentType: "text/html", Data: []byte("<script>1</script>")},
	}
	page := browserPage(msg)
	if !strings.Contains(page, `src="data:image/jpeg;base64,`) {
		t.Errorf("a jpeg must be shown:\n%s", page)
	}
	if strings.Contains(page, "data:image/svg") || strings.Contains(page, "data:text/html") {
		t.Errorf("only plain picture formats may be embedded:\n%s", page)
	}
	if !strings.Contains(page, "drawing.svg") || !strings.Contains(page, "page.html") {
		t.Error("the others must be listed by name")
	}
}

// frameTag returns the opening <iframe ...> tag up to its srcdoc.
func frameTag(t *testing.T, page string) string {
	t.Helper()
	_, after, ok := strings.Cut(page, "<iframe")
	if !ok {
		t.Fatalf("page has no frame:\n%s", page)
	}
	tag, _, _ := strings.Cut(after, "srcdoc=")
	return tag
}

func TestBrowserPage_remoteContentIsOffUntilAsked(t *testing.T) {
	msg := browserMsg()
	msg.HTMLBody = `<p><img src="https://track.example/pixel.gif"><a href="https://phish.example">click</a></p>`
	page := browserPage(msg)
	body := frameContent(t, page)

	// The frame's first words are a policy that lets nothing in from the
	// network: only pictures the mail carries itself, and its own styling.
	if !strings.HasPrefix(body, remoteContentGuard) {
		t.Fatalf("the framed mail must start with the guard, got %.120q", body)
	}
	for _, want := range []string{"default-src 'none'", "img-src data:", "style-src 'unsafe-inline'"} {
		if !strings.Contains(remoteContentGuard, want) {
			t.Errorf("guard %q lacks %q", remoteContentGuard, want)
		}
	}
	for _, source := range []string{"http:", "https:", "*"} {
		if strings.Contains(remoteContentGuard, source) {
			t.Errorf("guard %q allows a network source (%s)", remoteContentGuard, source)
		}
	}
	// Links cannot open anything while the guard is on.
	_, rest, _ := strings.Cut(frameTag(t, page), ` sandbox="`)
	sandbox, _, _ := strings.Cut(rest, `"`)
	if sandbox != "allow-same-origin" {
		t.Errorf("sandbox = %q; until asked the frame may open nothing and run nothing", sandbox)
	}
	// The mail itself is still there, untouched, for when the reader says yes.
	if !strings.Contains(body, "https://track.example/pixel.gif") || !strings.Contains(body, "https://phish.example") {
		t.Errorf("the mail's own HTML must not be rewritten: %q", body)
	}
}

func TestBrowserPage_offersToLoadRemoteContent(t *testing.T) {
	msg := browserMsg()
	msg.HTMLBody = `<p>hi</p>`
	page := browserPage(msg)
	if !strings.Contains(page, `<button id="allow"`) || !strings.Contains(page, "Load pictures and enable links") {
		t.Errorf("an HTML mail's page must offer the button:\n%s", page)
	}
	if !strings.Contains(page, fmt.Sprintf(`data-guard-len="%d"`, len(remoteContentGuard))) {
		t.Errorf("the page must know how much of the frame is the guard:\n%s", page)
	}
}

func TestBrowserPage_plainTextMailHasNothingToUnblock(t *testing.T) {
	msg := browserMsg()
	msg.TextBody = "see https://example.com"
	if page := browserPage(msg); strings.Contains(page, `id="allow"`) {
		t.Errorf("a plain-text page loads nothing and needs no button:\n%s", page)
	}
}

func TestBrowserPage_embeddedPicturesShowWithoutAsking(t *testing.T) {
	msg := browserMsg()
	msg.HTMLBody = `<img src="cid:logo">`
	msg.Attachments = []*mail.Attachment{{ContentType: "image/png", ContentID: "logo", Inline: true, Data: []byte("x")}}
	if body := frameContent(t, browserPage(msg)); !strings.Contains(body, `src="data:image/png;base64,`) {
		t.Errorf("pictures the mail carries need no network and must show at once: %q", body)
	}
}
