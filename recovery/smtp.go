package recovery

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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
	if !validAddress(to) || strings.ContainsAny(code, "\r\n") || strings.ContainsAny(resetURL, "\r\n") {
		return errors.New("recovery: invalid message")
	}
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
	if strings.IndexFunc(s.from+to, func(r rune) bool { return r > 127 }) >= 0 {
		if supported, _ := client.Extension("SMTPUTF8"); !supported {
			return errors.New("recovery: SMTP server does not support international addresses")
		}
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: StillWorld password reset\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
		"Someone requested a password reset for your account.\r\n"+
		"Open %s and enter this code:\r\n\r\n%s\r\n\r\n"+
		"The code expires 30 minutes after it was requested. If you did not request this, ignore this message.\r\n",
		s.from, to, resetURL, code)
	if _, err := writer.Write([]byte(message)); err != nil {
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close SMTP session: %w", err)
	}
	return nil
}
