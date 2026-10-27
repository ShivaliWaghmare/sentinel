package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildEmailMIME(t *testing.T) {
	app, run, results := failedRun()
	r := NewReport(app, run, results, "http://localhost:3000")
	date := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	raw, err := buildEmail(r, "sentinel@example.com", []string{"a@example.com", "b@example.com"}, date)
	if err != nil {
		t.Fatalf("buildEmail: %v", err)
	}

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != "❌ ShopLite: 6/8 passed (run #12)" {
		t.Errorf("Subject = %q (%v)", subject, err)
	}
	if got := msg.Header.Get("Subject"); strings.ContainsFunc(got, func(r rune) bool { return r > 127 }) {
		t.Errorf("raw Subject %q is not 7-bit", got)
	}
	if got := msg.Header.Get("To"); got != "a@example.com, b@example.com" {
		t.Errorf("To = %q", got)
	}
	if msg.Header.Get("MIME-Version") != "1.0" || msg.Header.Get("Date") == "" || msg.Header.Get("From") == "" {
		t.Errorf("missing headers: %v", msg.Header)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v), want multipart/alternative", mediaType, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	bodies := map[string]string{}
	for {
		p, err := mr.NextPart() // decodes quoted-printable
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		b, _ := io.ReadAll(p)
		ct := p.Header.Get("Content-Type")
		types = append(types, ct)
		bodies[ct] = string(b)
	}
	if len(types) != 2 || types[0] != "text/plain; charset=utf-8" || types[1] != "text/html; charset=utf-8" {
		t.Fatalf("parts = %v, want text/plain then text/html", types)
	}

	text := bodies["text/plain; charset=utf-8"]
	for _, want := range []string{"❌ ShopLite: 6/8 passed", "Flaky: 1", "- cart › badge <count> & total", "View run: http://localhost:3000/runs/12"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part missing %q:\n%s", want, text)
		}
	}
	html := bodies["text/html; charset=utf-8"]
	for _, want := range []string{"cart › badge &lt;count&gt; &amp; total", `href="http://localhost:3000/runs/12"`, "View run", `style="`} {
		if !strings.Contains(html, want) {
			t.Errorf("html part missing %q", want)
		}
	}
	if strings.Contains(html, "<style") || strings.Contains(html, "<count>") {
		t.Error("html has a <style> block or unescaped test title")
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes exceeds SMTP's 998 limit", len(line))
		}
	}
}

// fakeSMTP is a just-enough SMTP server for one conversation. It offers
// STARTTLS (if starttls is set), then AUTH PLAIN once TLS is up, and records
// what the client sent.
type fakeSMTP struct {
	addr     string
	starttls bool
	tls      *tls.Config

	mu       sync.Mutex
	auth     string // decoded AUTH PLAIN payload
	from     string
	rcpts    []string
	data     string
	commands []string
	done     chan struct{}
}

func startFakeSMTP(t *testing.T, starttls bool) (*fakeSMTP, *tls.Config) {
	t.Helper()
	// httptest's TLS server carries a self-signed certificate valid for
	// 127.0.0.1; borrow it for SMTP and trust it on the client side.
	certSrv := httptest.NewTLSServer(nil)
	t.Cleanup(certSrv.Close)
	roots := certSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String(), starttls: starttls, tls: certSrv.TLS, done: make(chan struct{})}
	go f.serve(ln)
	return f, &tls.Config{RootCAs: roots}
}

func (f *fakeSMTP) serve(ln net.Listener) {
	defer close(f.done)
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	tp := textproto.NewConn(conn)
	tp.PrintfLine("220 fake ESMTP")
	secure := false
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		f.mu.Lock()
		f.commands = append(f.commands, strings.ToUpper(verb))
		f.mu.Unlock()
		switch strings.ToUpper(verb) {
		case "EHLO":
			switch {
			case secure:
				tp.PrintfLine("250-fake\r\n250 AUTH PLAIN")
			case f.starttls:
				tp.PrintfLine("250-fake\r\n250 STARTTLS")
			default:
				tp.PrintfLine("250-fake\r\n250 AUTH PLAIN")
			}
		case "STARTTLS":
			tp.PrintfLine("220 go ahead")
			tlsConn := tls.Server(conn, f.tls)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			tp = textproto.NewConn(tlsConn)
			secure = true
		case "AUTH":
			_, payload, _ := strings.Cut(arg, " ")
			decoded, _ := base64.StdEncoding.DecodeString(payload)
			f.mu.Lock()
			f.auth = string(decoded)
			f.mu.Unlock()
			tp.PrintfLine("235 ok")
		case "MAIL":
			f.mu.Lock()
			f.from = arg
			f.mu.Unlock()
			tp.PrintfLine("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcpts = append(f.rcpts, arg)
			f.mu.Unlock()
			tp.PrintfLine("250 ok")
		case "DATA":
			tp.PrintfLine("354 send it")
			b, _ := io.ReadAll(tp.DotReader())
			f.mu.Lock()
			f.data = string(b)
			f.mu.Unlock()
			tp.PrintfLine("250 queued")
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("502 unknown")
		}
	}
}

func (f *fakeSMTP) sender(clientTLS *tls.Config) emailSender {
	host, port, _ := net.SplitHostPort(f.addr)
	p, _ := strconv.Atoi(port)
	return emailSender{
		host: host, port: p, user: "me@example.com", pass: "app-password",
		from: "me@example.com", to: []string{"a@example.com", "b@example.com"},
		timeout: 5 * time.Second, tlsConfig: clientTLS,
	}
}

func TestEmailSendOverSTARTTLS(t *testing.T) {
	f, clientTLS := startFakeSMTP(t, true)
	if err := f.sender(clientTLS).send(context.Background(), sampleReport("http://d")); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-f.done

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "\x00me@example.com\x00app-password" {
		t.Errorf("AUTH PLAIN payload = %q", f.auth)
	}
	if f.from != "FROM:<me@example.com>" || len(f.rcpts) != 2 {
		t.Errorf("MAIL %q, RCPT %v", f.from, f.rcpts)
	}
	msg, err := mail.ReadMessage(bufio.NewReader(strings.NewReader(f.data)))
	if err != nil || !strings.HasPrefix(msg.Header.Get("Content-Type"), "multipart/alternative") {
		t.Errorf("received message is not multipart/alternative: %v", err)
	}
	if got := strings.Join(f.commands, " "); !strings.HasPrefix(got, "EHLO STARTTLS EHLO AUTH") {
		t.Errorf("commands = %s, want STARTTLS before AUTH", got)
	}
}

func TestEmailRefusesWithoutSTARTTLS(t *testing.T) {
	f, clientTLS := startFakeSMTP(t, false)
	err := f.sender(clientTLS).send(context.Background(), sampleReport("http://d"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want a STARTTLS error", err)
	}
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "" || f.data != "" {
		t.Error("credentials or message were sent without TLS")
	}
}

func TestSplitAddresses(t *testing.T) {
	got := splitAddresses(" a@x.com, ,b@y.com ,")
	if strings.Join(got, "|") != "a@x.com|b@y.com" {
		t.Errorf("splitAddresses = %q", got)
	}
}
