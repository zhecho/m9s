package smtp

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/zhecho/m9s/internal/config"
)

// Draft holds the fields needed to compose an email.
type Draft struct {
	From       string
	To         []string
	CC         []string
	BCC        []string
	ReplyTo    string
	Subject    string
	Body       string
	InReplyTo  string
	References string
}

// Sender sends email via SMTP.
// ErrAuth is wrapped by the error returned when the server refuses the
// user name or password.
var ErrAuth = errors.New("auth")

type Sender interface {
	// Send delivers d and returns the message exactly as it was sent, so
	// the caller can keep a copy in the Sent folder.
	Send(password string, d Draft) ([]byte, error)
}

type sender struct {
	cfg *config.SMTPConfig
}

// New creates a new Sender.
func New(cfg *config.SMTPConfig) Sender {
	return &sender{cfg: cfg}
}

// dialAndAuth dials the SMTP server and authenticates. The caller must call
// c.Quit() when done. Uses implicit TLS when cfg.TLS is true, STARTTLS otherwise.
func dialAndAuth(cfg *config.SMTPConfig, password string) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	var c *smtp.Client

	if cfg.TLS {
		// Implicit TLS (port 465 / SMTPS)
		tlsConn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return nil, fmt.Errorf("dial failed: %w", err)
		}
		c, err = smtp.NewClient(tlsConn, cfg.Host)
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("smtp client: %w", err)
		}
	} else {
		// STARTTLS (port 587)
		var err error
		c, err = smtp.Dial(addr)
		if err != nil {
			return nil, fmt.Errorf("dial failed: %w", err)
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			c.Close()
			return nil, fmt.Errorf("StartTLS: %w", err)
		}
	}

	if err := c.Auth(smtp.PlainAuth("", cfg.User, password, cfg.Host)); err != nil {
		c.Quit() //nolint:errcheck
		return nil, fmt.Errorf("%w: %w", ErrAuth, err)
	}
	return c, nil
}

// Probe dials and authenticates but sends no mail. Returns nil on success.
// Used by the `m9s check` CLI command to verify SMTP config.
func Probe(cfg *config.SMTPConfig, password string) error {
	c, err := dialAndAuth(cfg, password)
	if err != nil {
		return err
	}
	return c.Quit()
}

func (s *sender) Send(password string, d Draft) ([]byte, error) {
	c, err := dialAndAuth(s.cfg, password)
	if err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}
	defer c.Quit() //nolint:errcheck

	// MAIL FROM requires a bare address.
	fromAddr := s.cfg.User
	if parsed, err := mail.ParseAddress(d.From); err == nil {
		fromAddr = parsed.Address
	}
	if err := c.Mail(fromAddr); err != nil {
		return nil, fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, to := range d.To {
		if err := c.Rcpt(to); err != nil {
			return nil, fmt.Errorf("RCPT TO %s: %w", to, err)
		}
	}
	for _, cc := range d.CC {
		if err := c.Rcpt(cc); err != nil {
			return nil, fmt.Errorf("RCPT TO %s: %w", cc, err)
		}
	}

	for _, bcc := range d.BCC {
		if err := c.Rcpt(bcc); err != nil {
			return nil, fmt.Errorf("RCPT TO %s: %w", bcc, err)
		}
	}

	msg, err := BuildMessage(d)
	if err != nil {
		return nil, err
	}
	wc, err := c.Data()
	if err != nil {
		return nil, fmt.Errorf("DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return nil, fmt.Errorf("write message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return nil, err
	}
	return msg, nil
}

// SentCopy returns msg as it should be kept in the Sent folder: the same
// bytes plus a Bcc header naming the blind recipients, which the sent
// message must not carry but the sender's own copy should.
func SentCopy(msg []byte, d Draft) []byte {
	if len(d.BCC) == 0 {
		return msg
	}
	bcc := fmt.Sprintf("Bcc: %s\r\n", headerValue(strings.Join(d.BCC, ", ")))
	out := make([]byte, 0, len(msg)+len(bcc))
	out = append(out, bcc...)
	return append(out, msg...)
}

// BuildMessage renders a Draft as an RFC 5322 message ready for SMTP DATA.
// It generates a unique Message-ID on the sender's domain (receivers such as
// Gmail reject mail without one) and RFC 2047-encodes non-ASCII headers.
// Bcc recipients are deliberately not written to the headers.
func BuildMessage(d Draft) ([]byte, error) {
	from := d.From
	domain := "localhost"
	if parsed, err := mail.ParseAddress(d.From); err == nil {
		from = parsed.String()
		if i := strings.LastIndex(parsed.Address, "@"); i >= 0 {
			domain = parsed.Address[i+1:]
		}
	}
	msgID, err := newMessageID(domain)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(d.To, ", "))
	if len(d.CC) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(d.CC, ", "))
	}
	if d.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", d.ReplyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", d.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", msgID)
	if d.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", d.InReplyTo)
	}
	if d.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", d.References)
	}
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&b, "\r\n%s", d.Body)
	return b.Bytes(), nil
}

func newMessageID(domain string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("message-id: %w", err)
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().Unix(), hex.EncodeToString(buf), domain), nil
}

// headerValue keeps a header on one line: a CR or LF inside a value would
// start a new header (or, from an editor, leave a stray CR in the value).
func headerValue(v string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(v))
}

// FormatDraft serialises a Draft as the .eml handed to $EDITOR and kept on
// disk. It uses plain LF line endings throughout: mixing CRLF headers with an
// LF body makes editors such as vim show ^M and keep stray CRs.
func FormatDraft(d Draft) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\n", headerValue(d.From))
	fmt.Fprintf(&b, "To: %s\n", headerValue(strings.Join(d.To, ", ")))
	if len(d.CC) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", headerValue(strings.Join(d.CC, ", ")))
	}
	fmt.Fprintf(&b, "Subject: %s\n", headerValue(d.Subject))
	fmt.Fprintf(&b, "Date: %s\n", time.Now().UTC().Format(time.RFC1123Z))
	if d.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\n", headerValue(d.InReplyTo))
	}
	if d.References != "" {
		fmt.Fprintf(&b, "References: %s\n", headerValue(d.References))
	}
	fmt.Fprintf(&b, "\n%s", normaliseNewlines(d.Body))
	return b.Bytes(), nil
}

// FormatDraftForUpload serialises a draft for the server's Drafts folder as
// a standard message: CRLF line endings, non-ASCII header text as RFC 2047
// encoded words (raw UTF-8 headers are rejected by strict servers) and a
// declared UTF-8 body.
func FormatDraftForUpload(d Draft) ([]byte, error) {
	var b bytes.Buffer
	header := func(name, value string) {
		fmt.Fprintf(&b, "%s: %s\r\n", name, value)
	}
	header("From", encodeAddressList([]string{d.From}))
	header("To", encodeAddressList(d.To))
	if len(d.CC) > 0 {
		header("Cc", encodeAddressList(d.CC))
	}
	header("Subject", encodeText(headerValue(d.Subject)))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	if d.InReplyTo != "" {
		header("In-Reply-To", headerValue(d.InReplyTo))
	}
	if d.References != "" {
		header("References", headerValue(d.References))
	}
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(normaliseNewlines(d.Body), "\n", "\r\n"))
	return b.Bytes(), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// encodeText returns s as RFC 2047 encoded words if it is not plain ASCII.
func encodeText(s string) string {
	if isASCII(s) {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

// encodeAddressList encodes display names of addresses that need it.
func encodeAddressList(addrs []string) string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = headerValue(a)
		if a == "" {
			continue
		}
		if isASCII(a) {
			out = append(out, a)
		} else if parsed, err := mail.ParseAddress(a); err == nil {
			out = append(out, parsed.String()) // encodes a non-ASCII name
		} else {
			out = append(out, encodeText(a))
		}
	}
	return strings.Join(out, ", ")
}

// normaliseNewlines turns CRLF, stray CRs and lone CRs into plain LF.
func normaliseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	return strings.Join(lines, "\n")
}

// ParseDraft parses an edited .eml back into a Draft. It is forgiving about
// what editors do: CRLF or LF line endings, stray CRs, a header wrapped onto
// a continuation line, and RFC 2047 encoded words.
func ParseDraft(data []byte) (Draft, error) {
	lines := strings.Split(normaliseNewlines(string(data)), "\n")
	type field struct{ name, value string }
	var fields []field
	i := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			break
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			last := &fields[len(fields)-1]
			last.value += " " + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(name, " \t") {
			continue
		}
		fields = append(fields, field{strings.ToLower(name), strings.TrimSpace(value)})
	}
	var dec mime.WordDecoder
	decode := func(v string) string {
		if out, err := dec.DecodeHeader(v); err == nil {
			return out
		}
		return v
	}
	var d Draft
	for _, f := range fields {
		switch f.name {
		case "from":
			d.From = decode(f.value)
		case "to":
			d.To = splitAddresses(decode(f.value))
		case "cc":
			d.CC = splitAddresses(decode(f.value))
		case "subject":
			d.Subject = decode(f.value)
		case "in-reply-to":
			d.InReplyTo = f.value
		case "references":
			d.References = f.value
		}
	}
	if i <= len(lines) {
		d.Body = strings.Join(lines[min(i, len(lines)):], "\n")
	}
	return d, nil
}

func splitAddresses(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}
