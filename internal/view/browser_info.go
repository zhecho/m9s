package view

import (
	"fmt"
	"html"
	"mime"
	"regexp"
	"strings"

	"github.com/zhecho/m9s/internal/mail"
)

// browserInfoButton opens and closes the Email headers panel. It sits next
// to Load pictures in the guard bar, or in a bar of its own on a page
// without a guard.
const browserInfoButton = `<button id="info-toggle" type="button" aria-expanded="false" aria-controls="info">Email headers</button>`

// browserInfoScript makes browserInfoButton open and close the panel.
const browserInfoScript = `
(function () {
  const t = document.getElementById('info-toggle'), p = document.getElementById('info');
  if (!t || !p) return;
  t.addEventListener('click', () => {
    const open = p.hidden;
    p.hidden = !open;
    t.setAttribute('aria-expanded', String(open));
  });
})();
`

// browserInfoRules style the button bar and the panel: neutral in Original,
// the theme's colours in the other modes.
const browserInfoRules = `
.bar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; padding: 8px 16px; background: #f4f5f7; border-bottom: 1px solid #d9dce1; }
.bar button { font: inherit; padding: 4px 12px; border: 1px solid #8a6d00; border-radius: 4px; background: #fff; color: #1a1a1a; cursor: pointer; }
#info-toggle[aria-expanded=true] { background: #1a1a1a; border-color: #1a1a1a; color: #fff; }
.info { padding: 12px 16px; background: #fafbfc; border-bottom: 1px solid #d9dce1; }
.info table { border-collapse: collapse; margin-bottom: 12px; }
.info th { text-align: left; font-weight: 600; color: #5b6470; padding: 1px 12px 1px 0; vertical-align: top; white-space: nowrap; }
.info td { padding: 1px 0; word-break: break-word; }
.info h2 { margin: 0 0 6px; font-size: 13px; color: #5b6470; }
.info pre { margin: 0; padding: 8px; max-height: 60vh; overflow: auto; font-size: 12px; word-break: break-all; border: 1px solid #d9dce1; border-radius: 4px; }
:root:not([data-theme=original]) .bar { background: var(--head-bg, #f4f5f7); border-bottom-color: var(--border, #d9dce1); }
:root:not([data-theme=original]) .info { background: var(--bg, #fafbfc); border-bottom-color: var(--border, #d9dce1); }
:root:not([data-theme=original]) .info th, :root:not([data-theme=original]) .info h2 { color: var(--label, #5b6470); }
:root:not([data-theme=original]) .info pre { border-color: var(--border, #d9dce1); }
:root:not([data-theme=original]) #info-toggle[aria-expanded=true] { background: var(--accent, #1a1a1a); color: var(--bg, #fff); }
`

// browserInfoPanel is the Email headers panel: a summary of what a reader
// checks first, then every header field exactly as received. All of it is
// the sender's text and is written as text only.
func browserInfoPanel(msg *mail.Message) string {
	var b strings.Builder
	b.WriteString(`<section id="info" class="info" hidden>`)
	if len(msg.Headers) == 0 {
		b.WriteString("<p>The headers are not downloaded yet.</p></section>\n")
		return b.String()
	}
	b.WriteString("<table>\n")
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "<tr><th>%s</th><td>%s</td></tr>\n", label, html.EscapeString(value))
		}
	}
	row("Size", humanSize(msg.Size))
	row("Message-ID", strings.Trim(first(msg, "Message-ID"), "<>"))
	row("Return-Path", strings.Trim(first(msg, "Return-Path"), "<>"))
	row("Reply-To", decodeWords(first(msg, "Reply-To")))
	row("Sent with", decodeWords(firstOf(msg, "X-Mailer", "User-Agent")))
	row("Checks", authChecks(first(msg, "Authentication-Results")))
	row("Route", route(msg.HeaderValues("Received")))
	if len(msg.Parts) > 0 {
		var parts []string
		for _, p := range msg.Parts {
			line := strings.Repeat("  ", p.Depth) + p.ContentType
			if p.Filename != "" {
				line += " " + p.Filename
			}
			if p.Size > 0 {
				line += " " + humanSize(p.Size)
			}
			parts = append(parts, html.EscapeString(line))
		}
		fmt.Fprintf(&b, "<tr><th>Parts</th><td><pre>%s</pre></td></tr>\n", strings.Join(parts, "\n"))
	}
	b.WriteString("</table>\n<h2>All headers, exactly as received</h2>\n<pre>")
	for i, f := range msg.Headers {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(html.EscapeString(strings.ReplaceAll(f.Raw, "\r\n", "\n")))
	}
	b.WriteString("</pre></section>\n")
	return b.String()
}

// first is the topmost value of a header field ("" without one).
func first(msg *mail.Message, key string) string {
	if v := msg.HeaderValues(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// firstOf is the topmost value of the first of keys the mail has.
func firstOf(msg *mail.Message, keys ...string) string {
	for _, k := range keys {
		if v := first(msg, k); v != "" {
			return v
		}
	}
	return ""
}

// decodeWords decodes RFC 2047 encoded words; on failure it keeps s.
func decodeWords(s string) string {
	if d, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		return d
	}
	return s
}

var (
	authResult = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)=([a-z]+)`)
	dkimDomain = regexp.MustCompile(`(?i)\bheader\.(?:d|i)=@?([^\s;]+)`)
	fromHost   = regexp.MustCompile(`(?i)^from\s+([^\s;()]+)`)
	byHost     = regexp.MustCompile(`(?i)\bby\s+([^\s;()]+)`)
)

// authChecks summarises the receiving server's Authentication-Results:
// "SPF pass · DKIM pass (example.com) · DMARC fail". Each method once, as the
// topmost (receiving) server reported it.
func authChecks(results string) string {
	var out []string
	seen := map[string]bool{}
	for _, m := range authResult.FindAllStringSubmatch(results, -1) {
		method := strings.ToUpper(m[1])
		if seen[method] {
			continue
		}
		seen[method] = true
		text := method + " " + strings.ToLower(m[2])
		if method == "DKIM" {
			if d := dkimDomain.FindStringSubmatch(results); d != nil {
				text += " (" + d[1] + ")"
			}
		}
		out = append(out, text)
	}
	return strings.Join(out, " · ")
}

// route is the way the mail came, from the Received fields (newest on top):
// "2 hops: laptop → mail.example.com → mx.google.com", oldest first.
func route(received []string) string {
	if len(received) == 0 {
		return ""
	}
	var hosts []string
	add := func(h string) {
		if h != "" && (len(hosts) == 0 || hosts[len(hosts)-1] != h) {
			hosts = append(hosts, h)
		}
	}
	for i := len(received) - 1; i >= 0; i-- {
		if i == len(received)-1 {
			if m := fromHost.FindStringSubmatch(received[i]); m != nil {
				add(m[1])
			}
		}
		if m := byHost.FindStringSubmatch(received[i]); m != nil {
			add(m[1])
		}
	}
	hops := "hops"
	if len(received) == 1 {
		hops = "hop"
	}
	return fmt.Sprintf("%d %s: %s", len(received), hops, strings.Join(hosts, " → "))
}
