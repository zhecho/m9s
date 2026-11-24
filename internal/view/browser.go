package view

import (
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/zhecho/m9s/internal/mail"
)

// cidRef matches a reference to a picture carried inside the mail.
var cidRef = regexp.MustCompile(`(?i)cid:[^"'\s>)]+`)

const browserPageStyle = `
:root { color-scheme: light; }
body { margin: 0; background: #fff; color: #1a1a1a; font: 14px/1.45 -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; }
.head { padding: 12px 16px; background: #f4f5f7; border-bottom: 1px solid #d9dce1; }
.head h1 { margin: 0 0 8px; font-size: 18px; }
.head table { border-collapse: collapse; }
.head th { text-align: left; font-weight: 600; color: #5b6470; padding: 1px 12px 1px 0; vertical-align: top; }
.head td { padding: 1px 0; word-break: break-word; }
.guard { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; padding: 8px 16px; background: #fff8e1; border-bottom: 1px solid #ead9a0; }
.guard.nudge { background: #ffe9a8; }
.guard button { font: inherit; padding: 4px 12px; border: 1px solid #8a6d00; border-radius: 4px; background: #fff; color: #1a1a1a; cursor: pointer; }
.guard button:hover { background: #fdf3cf; }
iframe { display: block; width: 100%; height: 85vh; border: 0; }
pre { margin: 0; padding: 16px; white-space: pre-wrap; word-break: break-word; font: 14px/1.5 ui-monospace, Menlo, Consolas, monospace; }
.files { padding: 12px 16px; border-top: 1px solid #d9dce1; }
.files h2 { margin: 0 0 8px; font-size: 14px; color: #5b6470; }
.files figure { margin: 0 0 16px; }
.files img { max-width: 100%; height: auto; }
.files figcaption, .files li { color: #5b6470; }
`

// remoteContentGuard is the first thing in the mail's frame until the reader
// presses the button: a policy under which the browser fetches nothing from
// the network for this mail — no pictures, style sheets, fonts or media —
// while pictures the mail carries itself (inlined as data:) and its own
// styling still work. The mail cannot undo it: the policy comes first, and
// the frame runs no scripts.
const remoteContentGuard = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; media-src data:">`

// frameSandbox is all the mail's frame may ever do: be measured and watched
// by the page around it. No scripts, no forms, no pop-ups, no steering the
// outer page — before and after the button.
const frameSandbox = "allow-same-origin"

// The mail's own frame cannot run scripts, so the page around it does two
// jobs for it: it sizes the frame to the mail (one scrollbar instead of two),
// and it handles every click on a link. Until the reader presses the button a
// link does nothing; after, web and mail links open in a new tab, so the mail's
// frame itself never goes anywhere. The button reloads the mail without the
// guard.
const browserPageScript = `
const f = document.getElementById('mail');
const bar = document.getElementById('guard');
let allowed = false;
function fit() { try { f.style.height = f.contentDocument.documentElement.scrollHeight + 'px'; } catch (e) {} }
f.addEventListener('load', () => {
  fit();
  try {
    const doc = f.contentDocument;
    for (const img of doc.images) img.addEventListener('load', fit);
    doc.addEventListener('click', (e) => {
      const link = e.target && e.target.closest ? e.target.closest('a, area') : null;
      if (!link) return;
      const href = link.getAttribute('href') || '';
      if (href.startsWith('#')) return; // a jump inside the mail
      e.preventDefault();
      if (!allowed) {
        bar.classList.add('nudge');
        setTimeout(() => bar.classList.remove('nudge'), 600);
      } else if (/^(https?|mailto):/i.test(link.href)) {
        window.open(link.href, '_blank', 'noopener,noreferrer');
      }
    }, true);
  } catch (e) {}
  setTimeout(fit, 500); setTimeout(fit, 2500);
});
window.addEventListener('resize', fit);
document.getElementById('allow').addEventListener('click', () => {
  allowed = true;
  f.srcdoc = f.srcdoc.slice(Number(f.dataset.guardLen));
  bar.remove();
});
`

// browserPage renders msg as a self-contained HTML page: who/when/subject on
// top, then the mail as its sender designed it (or its plain text), then the
// attached pictures and the names of the other attachments.
//
// The mail's HTML goes into a sandboxed frame: scripts in it never run, and
// its styles cannot reach the rest of the page. Pictures the mail carries
// itself (cid: references) are inlined when they are a known picture format.
// Everything the mail would fetch from the network, and its links, stay off
// until the reader presses the button on top of the page (remoteContentGuard).
func browserPage(msg *mail.Message) string {
	env := msg.Envelope
	if env == nil {
		env = &mail.Envelope{}
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n<style>%s</style></head><body>\n", html.EscapeString(env.Subject), browserPageStyle)

	fmt.Fprintf(&b, "<div class=\"head\"><h1>%s</h1><table>\n", html.EscapeString(env.Subject))
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "<tr><th>%s</th><td>%s</td></tr>\n", label, html.EscapeString(value))
		}
	}
	row("From", addressList(env.From))
	row("To", addressList(env.To))
	row("Cc", addressList(env.CC))
	if !env.Date.IsZero() {
		row("Date", env.Date.Format("Mon, 02 Jan 2006 15:04 -0700"))
	}
	b.WriteString("</table></div>\n")

	used := map[*mail.Attachment]bool{}
	switch {
	case strings.TrimSpace(msg.HTMLBody) != "":
		doc := remoteContentGuard + `<base target="_blank">` + inlineEmbedded(msg, used)
		b.WriteString("<div class=\"guard\" id=\"guard\"><span>Pictures from the internet and links are off, " +
			"so the sender cannot tell that you opened this mail.</span>" +
			"<button id=\"allow\" type=\"button\">Load pictures and enable links</button></div>\n")
		fmt.Fprintf(&b, "<iframe id=\"mail\" sandbox=\"%s\" data-guard-len=\"%d\" srcdoc=\"%s\"></iframe>\n",
			frameSandbox, len(remoteContentGuard), html.EscapeString(doc))
		fmt.Fprintf(&b, "<script>%s</script>\n", browserPageScript)
	case msg.TextBody != "":
		fmt.Fprintf(&b, "<pre>%s</pre>\n", html.EscapeString(msg.TextBody))
	default:
		b.WriteString("<pre>(this message has no text)</pre>\n")
	}

	var pictures, others []*mail.Attachment
	for _, a := range msg.Attachments {
		switch {
		case used[a]:
		case isPicture(a):
			pictures = append(pictures, a)
		default:
			others = append(others, a)
		}
	}
	if len(pictures)+len(others) > 0 {
		b.WriteString("<div class=\"files\"><h2>Attachments</h2>\n")
		for _, a := range pictures {
			fmt.Fprintf(&b, "<figure><img src=\"%s\" alt=\"\"><figcaption>%s</figcaption></figure>\n",
				html.EscapeString(dataURI(a)), html.EscapeString(a.Filename))
		}
		if len(others) > 0 {
			b.WriteString("<ul>\n")
			for _, a := range others {
				name := a.Filename
				if name == "" {
					name = "(unnamed)"
				}
				fmt.Fprintf(&b, "<li>%s</li>\n", html.EscapeString(name))
			}
			b.WriteString("</ul>\n")
		}
		b.WriteString("</div>\n")
	}
	b.WriteString("</body></html>\n")
	return b.String()
}

// inlineEmbedded returns the mail's HTML with every cid: reference to a part
// the mail carries replaced by that part's content, and marks those parts in
// used. References to parts the mail does not carry are left alone.
func inlineEmbedded(msg *mail.Message, used map[*mail.Attachment]bool) string {
	byID := map[string]*mail.Attachment{}
	for _, a := range msg.Attachments {
		if a.ContentID != "" {
			byID[strings.ToLower(a.ContentID)] = a
		}
	}
	if len(byID) == 0 {
		return msg.HTMLBody
	}
	return cidRef.ReplaceAllStringFunc(msg.HTMLBody, func(ref string) string {
		id := ref[len("cid:"):]
		if plain, err := url.PathUnescape(id); err == nil {
			id = plain
		}
		a, ok := byID[strings.ToLower(id)]
		if !ok || !isPicture(a) {
			return ref
		}
		used[a] = true
		return dataURI(a)
	})
}

// pictureTypes are the formats the page embeds. A part's content type is
// whatever the mail claims, so nothing of it is ever written into the page:
// it only selects one of these fixed strings. SVG is left out on purpose — it
// is a document, not just pixels.
var pictureTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/bmp": true,
}

// pictureType returns a's format if it is one the page may embed, else "".
func pictureType(a *mail.Attachment) string {
	mt, _, err := mime.ParseMediaType(a.ContentType)
	if err != nil {
		return ""
	}
	if mt == "image/jpg" {
		mt = "image/jpeg"
	}
	if !pictureTypes[mt] {
		return ""
	}
	return mt
}

func isPicture(a *mail.Attachment) bool { return pictureType(a) != "" }

// dataURI is a's content as a URL; only for parts that pass isPicture.
func dataURI(a *mail.Attachment) string {
	return "data:" + pictureType(a) + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

func addressList(addrs []mail.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// browserFiles keeps the pages handed to the browser in one private folder,
// created on first use and removed by cleanup. base "" means the system's
// temporary directory.
type browserFiles struct {
	base string
	mu   sync.Mutex
	dir  string
}

// write renders msg to a new page and returns its path.
func (bf *browserFiles) write(msg *mail.Message) (string, error) {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	if err := bf.ensureDir(); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(bf.dir, "mail-*.html") // 0600
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(browserPage(msg)); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

// ensureDir creates the private folder on first use. bf.mu is held.
func (bf *browserFiles) ensureDir() error {
	if bf.dir != "" {
		return nil
	}
	dir, err := os.MkdirTemp(bf.base, "m9s-browser-") // 0700: mail is private
	if err != nil {
		return err
	}
	bf.dir = dir
	return nil
}

// writeFile saves an attachment under its own name, in a fresh folder of its
// own inside the private one, so two files of the same name never collide.
// cleanup removes it with the pages.
func (bf *browserFiles) writeFile(name string, data []byte) (string, error) {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	if err := bf.ensureDir(); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(bf.dir, "att-")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := markDownloaded(f); err != nil { // never open a file macOS would not check
		f.Close()
		_ = os.Remove(p)
		return "", fmt.Errorf("cannot mark %s as downloaded: %w", name, err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return p, err
}

// cleanup removes every page written so far.
func (bf *browserFiles) cleanup() {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	if bf.dir != "" {
		_ = os.RemoveAll(bf.dir)
		bf.dir = ""
	}
}

// openInDefaultBrowser hands path to the desktop's default browser and does
// not wait for it.
func openInDefaultBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux", "freebsd", "openbsd", "netbsd":
		cmd = exec.Command("xdg-open", path)
	default:
		return fmt.Errorf("opening a browser is not supported on %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
