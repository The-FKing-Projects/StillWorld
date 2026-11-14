package recovery

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type smtpSender struct {
	address   string
	host      string
	from      string
	mode      string
	username  string
	password  string
	tlsConfig *tls.Config
}

func (s *smtpSender) SendReset(ctx context.Context, to, code, resetURL string) error {
	return s.send(ctx, to, code, resetURL, "StillWorld password reset",
		"Someone requested a password reset for your account.\r\n"+
			"Open %s and enter this code:\r\n\r\n%s\r\n\r\n"+
			"The code expires 30 minutes after it was requested. If you did not request this, ignore this message.\r\n")
}

func (s *smtpSender) SendVerification(ctx context.Context, to, code, verificationURL string) error {
	return s.send(ctx, to, code, verificationURL, "StillWorld email verification",
		"Someone requested email verification for a StillWorld account.\r\n"+
			"Open %s and enter this code:\r\n\r\n%s\r\n\r\n"+
			"The code expires 30 minutes after it was requested. If you did not request this, ignore this message.\r\n")
}

func (s *smtpSender) SendSignup(ctx context.Context, to, code, signupURL string) error {
	return s.send(ctx, to, code, signupURL, "StillWorld account signup",
		"Complete your StillWorld account signup.\r\n"+
			"Open %s and enter this code:\r\n\r\n%s\r\n\r\n"+
			"The code expires 30 minutes after it was requested. If you did not request this, ignore this message.\r\n")
}

func (s *smtpSender) send(ctx context.Context, to, code, pageURL, subject, bodyFormat string) error {
	// Envelope recipient only: the user address is validated for SMTP RCPT and
	// must not be written into the DATA payload. CodeQL go/email-injection has
	// no sanitizer barriers, so any tainted value written to Client.Data is an
	// alert; keep DATA limited to configured From/Subject and trusted body parts.
	recipient, err := envelopeRecipient(to)
	if err != nil || !validAddress(s.from) || strings.ContainsAny(code, "\r\n") ||
		strings.ContainsAny(pageURL, "\r\n") || strings.ContainsAny(subject, "\r\n") ||
		strings.Count(bodyFormat, "%s") != 2 || strings.ContainsAny(bodyFormat, "\x00") {
		return errors.New("recovery: invalid message")
	}
	body := fmt.Sprintf(bodyFormat, pageURL, code)
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(20 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	rawConn := conn
	stopCancel := context.AfterFunc(ctx, func() { _ = rawConn.SetDeadline(time.Now()) })
	defer stopCancel()
	cfg := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		cfg = s.tlsConfig.Clone()
	}
	if s.mode == "tls" {
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("secure SMTP connection: %w", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("open SMTP session: %w", err)
	}
	defer client.Close()
	if s.mode == "starttls" {
		if available, _ := client.Extension("STARTTLS"); !available {
			return errors.New("recovery: SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(cfg); err != nil {
			return fmt.Errorf("secure SMTP session: %w", err)
		}
	}
	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if strings.IndexFunc(s.from+recipient, func(r rune) bool { return r > 127 }) >= 0 {
		if supported, _ := client.Extension("SMTPUTF8"); !supported {
			return errors.New("recovery: SMTP server does not support international addresses")
		}
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	headers := "From: " + s.from + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"
	if _, err := writer.Write([]byte(headers)); err != nil {
		return fmt.Errorf("write SMTP headers: %w", err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		return fmt.Errorf("write SMTP body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close SMTP session: %w", err)
	}
	return nil
}

// envelopeRecipient returns a single mailbox address suitable for SMTP RCPT.
func envelopeRecipient(to string) (string, error) {
	if !validAddress(to) {
		return "", errors.New("recovery: invalid recipient")
	}
	addr, err := mail.ParseAddress(to)
	if err != nil || addr.Name != "" || addr.Address != to {
		return "", errors.New("recovery: invalid recipient")
	}
	return addr.Address, nil
}
