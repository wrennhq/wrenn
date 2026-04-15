// Package email provides transactional email sending via SMTP.
//
// Emails are rendered from embedded Go templates (html/template + text/template)
// and sent as multipart/alternative MIME messages. When SMTP is not configured
// (Host is empty), a no-op mailer is returned that logs and discards.
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// Config holds SMTP connection credentials. All fields except Host are
// optional — omitting Host disables email entirely (no-op mailer).
type Config struct {
	Host      string // SMTP server hostname
	Port      int    // SMTP server port (default 587)
	Username  string // SMTP auth username
	Password  string // SMTP auth password
	FromEmail string // envelope sender address
}

// Mailer sends transactional emails.
type Mailer interface {
	Send(ctx context.Context, to string, subject string, data EmailData) error
}

// EmailData is the generic payload for all transactional emails.
// Templates conditionally render each field based on presence.
type EmailData struct {
	RecipientName string  // optional — used after "Hello"
	Message       string  // main body (plain text; HTML template wraps it)
	Button        *Button // optional CTA button
	Closing       string  // optional closing/footer message
}

// Button represents a call-to-action link rendered as a button in HTML
// and as a plain URL in the text variant.
type Button struct {
	Text string // button label
	URL  string // target URL
}

// New constructs a Mailer. If cfg.Host is empty, returns a no-op mailer
// that logs at debug level and discards. Panics if templates fail to parse
// (indicates a build-time bug in embedded templates).
func New(cfg Config) Mailer {
	if cfg.Host == "" {
		slog.Info("email: SMTP not configured, using no-op mailer")
		return &noopMailer{}
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	tmpl := mustLoadTemplates()
	slog.Info("email: SMTP configured", "host", cfg.Host, "port", cfg.Port, "from", cfg.FromEmail)
	return &mailer{cfg: cfg, tmpl: tmpl}
}

// mailer is the live SMTP implementation.
type mailer struct {
	cfg  Config
	tmpl *templates
}

func (m *mailer) Send(ctx context.Context, to string, subject string, data EmailData) error {
	if data.Button != nil {
		u, err := url.Parse(data.Button.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid button URL scheme: %s", data.Button.URL)
		}
	}

	htmlBody, err := m.tmpl.renderHTML(data)
	if err != nil {
		return fmt.Errorf("render html: %w", err)
	}
	textBody, err := m.tmpl.renderText(data)
	if err != nil {
		return fmt.Errorf("render text: %w", err)
	}

	msg, err := buildMIME(m.cfg.FromEmail, to, subject, htmlBody, textBody)
	if err != nil {
		return fmt.Errorf("build mime: %w", err)
	}

	if err := m.send(to, msg); err != nil {
		return fmt.Errorf("send email to %s: %w", to, err)
	}

	slog.Info("email: sent", "to", to, "subject", subject)
	return nil
}

// send dials the SMTP server and delivers the message.
// Port 465 uses implicit TLS; all other ports use STARTTLS.
func (m *mailer) send(to string, msg []byte) error {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)

	if m.cfg.Port == 465 {
		return m.sendImplicitTLS(addr, auth, to, msg)
	}
	// STARTTLS (port 587 or other).
	return smtp.SendMail(addr, auth, m.cfg.FromEmail, []string{to}, msg)
}

// sendImplicitTLS handles port 465 (SMTPS) where the entire connection is TLS.
func (m *mailer) sendImplicitTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.cfg.Host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(m.cfg.FromEmail); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

// buildMIME assembles a multipart/alternative message with text and HTML parts.
// Both parts are quoted-printable encoded per RFC 2045.
func buildMIME(from, to, subject, htmlBody, textBody string) ([]byte, error) {
	var headerBuf bytes.Buffer
	var bodyBuf bytes.Buffer

	// Sanitize header values to prevent header injection.
	from = sanitizeHeader(from)
	to = sanitizeHeader(to)

	// Encode "From" with display name.
	encodedFrom := mime.QEncoding.Encode("utf-8", "Wrenn") + " <" + from + ">"

	// Build multipart body first to get the boundary.
	mw := multipart.NewWriter(&bodyBuf)

	// Text part (first = lowest preference per RFC 2046).
	textPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	qpw := quotedprintable.NewWriter(textPart)
	if _, err := qpw.Write([]byte(textBody)); err != nil {
		return nil, err
	}
	if err := qpw.Close(); err != nil {
		return nil, err
	}

	// HTML part (second = highest preference).
	htmlPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	qpw = quotedprintable.NewWriter(htmlPart)
	if _, err := qpw.Write([]byte(htmlBody)); err != nil {
		return nil, err
	}
	if err := qpw.Close(); err != nil {
		return nil, err
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}

	// Write headers.
	fmt.Fprintf(&headerBuf, "From: %s\r\n", encodedFrom)
	fmt.Fprintf(&headerBuf, "To: %s\r\n", to)
	fmt.Fprintf(&headerBuf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&headerBuf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&headerBuf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", mw.Boundary())
	fmt.Fprintf(&headerBuf, "\r\n")

	headerBuf.Write(bodyBuf.Bytes())
	return headerBuf.Bytes(), nil
}

// sanitizeHeader strips CR and LF characters to prevent SMTP header injection.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// noopMailer discards emails when SMTP is not configured.
type noopMailer struct{}

func (n *noopMailer) Send(_ context.Context, to string, subject string, _ EmailData) error {
	slog.Debug("email: no-op send", "to", to, "subject", subject)
	return nil
}
