package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// emailHTML uses inline styles only: many mail clients (Gmail included)
// drop <style> blocks. Colors are plain hex because email has no CSS
// variables to share with the dashboard.
var emailHTML = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html>
<body style="margin:0;padding:24px;background-color:#f4f4f5;font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#18181b;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:640px;margin:0 auto;background-color:#ffffff;border:1px solid #e4e4e7;border-radius:8px;">
<tr><td style="padding:24px 24px 8px 24px;">
<h1 style="margin:0;font-size:20px;line-height:28px;">{{.Emoji}} {{.Headline}}</h1>
<p style="margin:4px 0 0 0;font-size:13px;color:#71717a;">{{.Details}}</p>
</td></tr>
{{- if .RunError}}
<tr><td style="padding:16px 24px 0 24px;">
<p style="margin:0 0 4px 0;font-size:14px;font-weight:600;">Run error</p>
<pre style="margin:0;padding:8px;background-color:#fef2f2;border-radius:4px;font-family:Menlo,Consolas,monospace;font-size:12px;white-space:pre-wrap;color:#991b1b;">{{.RunError}}</pre>
</td></tr>
{{- end}}
{{- if .Failures}}
<tr><td style="padding:16px 24px 0 24px;">
<p style="margin:0 0 8px 0;font-size:14px;font-weight:600;">Failures</p>
{{- range .Failures}}
<div style="margin:0 0 12px 0;padding:0 0 0 12px;border-left:3px solid #dc2626;">
<p style="margin:0;font-size:14px;font-weight:600;">{{.Title}}</p>
{{- if .ErrorLine}}
<pre style="margin:4px 0 0 0;font-family:Menlo,Consolas,monospace;font-size:12px;white-space:pre-wrap;color:#52525b;">{{.ErrorLine}}</pre>
{{- end}}
{{- /* STEP 9 (triage): render the category and summary here, e.g. a colored
       badge "real_bug · 92%" followed by the one-line summary. */}}
</div>
{{- end}}
{{- if .MoreText}}
<p style="margin:0;font-size:13px;color:#71717a;">{{.MoreText}}</p>
{{- end}}
</td></tr>
{{- end}}
<tr><td style="padding:16px 24px 24px 24px;">
<a href="{{.RunURL}}" style="display:inline-block;padding:10px 16px;background-color:#18181b;color:#ffffff;text-decoration:none;border-radius:6px;font-size:14px;font-weight:600;">View run</a>
</td></tr>
</table>
<p style="margin:12px auto 0 auto;max-width:640px;font-size:12px;color:#a1a1aa;text-align:center;">Sent by Sentinel</p>
</body>
</html>
`))

// emailText is the plain-text alternative.
func emailText(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n%s\n", r.Emoji(), r.Headline(), r.Details())
	if r.RunError != "" {
		fmt.Fprintf(&b, "\nRun error:\n  %s\n", r.RunError)
	}
	if len(r.Failures) > 0 {
		b.WriteString("\nFailures:\n")
		for _, f := range r.Failures {
			fmt.Fprintf(&b, "- %s\n", f.Title)
			if f.ErrorLine != "" {
				fmt.Fprintf(&b, "  %s\n", f.ErrorLine)
			}
			// STEP 9 (triage): add "  Triage: <category> (<confidence>): <summary>".
		}
		if more := r.MoreText(); more != "" {
			fmt.Fprintf(&b, "%s\n", more)
		}
	}
	fmt.Fprintf(&b, "\nView run: %s\n", r.RunURL)
	return b.String()
}

// buildEmail returns a complete RFC 5322 message: headers plus a
// multipart/alternative body with text/plain first and text/html last, the
// order clients use to prefer HTML.
func buildEmail(r Report, from string, to []string, date time.Time) ([]byte, error) {
	var htmlBody bytes.Buffer
	if err := emailHTML.Execute(&htmlBody, r); err != nil {
		return nil, fmt.Errorf("email: render html: %w", err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	parts := []struct{ contentType, content string }{
		{"text/plain; charset=utf-8", emailText(r)},
		{"text/html; charset=utf-8", htmlBody.String()},
	}
	for _, p := range parts {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.contentType},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, fmt.Errorf("email: create part: %w", err)
		}
		// Quoted-printable keeps lines under SMTP's 998-byte limit and
		// carries UTF-8 (emoji) safely through 7-bit servers.
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(p.content)); err != nil {
			return nil, fmt.Errorf("email: write part: %w", err)
		}
		if err := qp.Close(); err != nil {
			return nil, fmt.Errorf("email: write part: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("email: close multipart: %w", err)
	}

	var msg bytes.Buffer
	headers := [][2]string{
		{"From", from},
		{"To", strings.Join(to, ", ")},
		// Q-encoding makes the emoji subject valid in a 7-bit header.
		{"Subject", mime.QEncoding.Encode("utf-8", r.Subject())},
		{"Date", date.Format(time.RFC1123Z)},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`},
	}
	for _, h := range headers {
		fmt.Fprintf(&msg, "%s: %s\r\n", h[0], h[1])
	}
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}

// emailSender sends reports over SMTP with STARTTLS, which Gmail (port 587,
// app password) and most providers use. Implicit TLS (port 465) is not
// supported.
type emailSender struct {
	host      string
	port      int
	user      string
	pass      string // a secret: never logged
	from      string
	to        []string
	timeout   time.Duration
	tlsConfig *tls.Config // nil in production; tests trust their own CA
}

func (e emailSender) send(ctx context.Context, r Report) error {
	msg, err := buildEmail(r, e.from, e.to, time.Now())
	if err != nil {
		return err
	}
	if err := e.deliver(ctx, msg); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return nil
}

// deliver runs the SMTP conversation by hand rather than with smtp.SendMail,
// so STARTTLS is required instead of used only when offered.
func (e emailSender) deliver(ctx context.Context, msg []byte) error {
	addr := net.JoinHostPort(e.host, strconv.Itoa(e.port))
	dialer := net.Dialer{Timeout: e.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	// One deadline for the whole conversation, so a stuck server can't hang a run.
	if err := conn.SetDeadline(time.Now().Add(e.timeout)); err != nil {
		conn.Close()
		return fmt.Errorf("set deadline: %w", err)
	}
	c, err := smtp.NewClient(conn, e.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("server does not offer STARTTLS; refusing to send credentials in plain text")
	}
	tlsConfig := &tls.Config{ServerName: e.host, MinVersion: tls.VersionTLS12}
	if e.tlsConfig != nil {
		tlsConfig = e.tlsConfig.Clone()
		tlsConfig.ServerName = e.host
	}
	if err := c.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}
	if err := c.Auth(smtp.PlainAuth("", e.user, e.pass, e.host)); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := c.Mail(e.from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range e.to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt to %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return c.Quit()
}

// splitAddresses parses EMAIL_TO's comma-separated list.
func splitAddresses(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}
