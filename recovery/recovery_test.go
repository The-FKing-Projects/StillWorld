package recovery

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestConfiguration(t *testing.T) {
	base := map[string]string{
		"STILLWORLD_RECOVERY_ORIGIN": "https://game.example",
		"STILLWORLD_SMTP_ADDRESS":    "mail.example:587",
		"STILLWORLD_SMTP_FROM":       "noreply@example.com",
	}
	load := func(values map[string]string) (*Config, error) {
		return fromLookup(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	}
	if cfg, err := load(nil); err != nil || cfg != nil {
		t.Fatalf("disabled: %v %v", cfg, err)
	}
	cfg, err := load(base)
	if err != nil || cfg.ResetURL() != "https://game.example/reset-password" {
		t.Fatalf("valid configuration: %v", err)
	}
	if err := cfg.ValidateForCookies(true); err == nil {
		t.Fatal("accepted a public origin that conflicts with the server scheme")
	}
	if err := cfg.ValidateForCookies(false); err != nil {
		t.Fatalf("matching scheme: %v", err)
	}
	for _, change := range []struct{ key, value string }{
		{"STILLWORLD_RECOVERY_ORIGIN", "http://game.example"},
		{"STILLWORLD_RECOVERY_ORIGIN", "https://game.example/path"},
		{"STILLWORLD_RECOVERY_ORIGIN", "https://user:secret@game.example"},
		{"STILLWORLD_RECOVERY_ORIGIN", "https://:443"},
		{"STILLWORLD_SMTP_ADDRESS", "mail.example"},
		{"STILLWORLD_SMTP_FROM", "a@example.com\r\nBcc: victim@example.com"},
		{"STILLWORLD_SMTP_MODE", "plain"},
		{"STILLWORLD_SMTP_PASSWORD", "unpaired"},
	} {
		copy := make(map[string]string, len(base))
		for k, v := range base {
			copy[k] = v
		}
		copy[change.key] = change.value
		if _, err := load(copy); err == nil {
			t.Errorf("accepted invalid %s", change.key)
		}
	}
	base["STILLWORLD_RECOVERY_ORIGIN"] = "http://127.0.0.1:8082"
	if _, err := load(base); err != nil {
		t.Fatalf("loopback origin: %v", err)
	}
}

func TestSMTPRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.StartTLS()
	defer server.Close()
	sender := &smtpSender{address: server.Listener.Addr().String(), host: "127.0.0.1", from: "sender@example.com", mode: "tls"}
	if err := sender.SendReset(context.Background(), "player@example.com", "code", "https://game.example/reset-password"); err == nil {
		t.Fatal("accepted an untrusted SMTP certificate")
	}
}

func TestSMTPRequiresTLSAndDeliversCode(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(certServer.Certificate())
	serverTLS := &tls.Config{Certificates: certServer.TLS.Certificates, MinVersion: tls.VersionTLS12}
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan string, 1)
			go func() {
				raw, err := listener.Accept()
				if err != nil {
					result <- "accept: " + err.Error()
					return
				}
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(5 * time.Second))
				conn := raw
				if mode == "tls" {
					conn = tls.Server(raw, serverTLS)
				}
				reader := textproto.NewReader(bufio.NewReader(conn))
				fmt.Fprint(conn, "220 test ESMTP\r\n")
				line, err := reader.ReadLine()
				if err != nil || !strings.HasPrefix(line, "EHLO ") {
					result <- "initial EHLO"
					return
				}
				if mode == "starttls" {
					fmt.Fprint(conn, "250-test\r\n250 STARTTLS\r\n")
					line, err = reader.ReadLine()
					if err != nil || line != "STARTTLS" {
						result <- "missing STARTTLS"
						return
					}
					fmt.Fprint(conn, "220 begin TLS\r\n")
					conn = tls.Server(raw, serverTLS)
					reader = textproto.NewReader(bufio.NewReader(conn))
					line, err = reader.ReadLine()
					if err != nil || !strings.HasPrefix(line, "EHLO ") {
						result <- "secure EHLO"
						return
					}
				}
				fmt.Fprint(conn, "250-test\r\n250 AUTH PLAIN\r\n")
				for _, prefix := range []string{"AUTH PLAIN ", "MAIL FROM:", "RCPT TO:", "DATA"} {
					line, err = reader.ReadLine()
					if err != nil || !strings.HasPrefix(line, prefix) {
						result <- "unexpected SMTP command"
						return
					}
					switch prefix {
					case "AUTH PLAIN ":
						fmt.Fprint(conn, "235 authenticated\r\n")
					case "DATA":
						fmt.Fprint(conn, "354 send data\r\n")
					default:
						fmt.Fprint(conn, "250 ok\r\n")
					}
				}
				lines, err := reader.ReadDotLines()
				if err != nil {
					result <- "message read failed"
					return
				}
				fmt.Fprint(conn, "250 accepted\r\n")
				_, err = reader.ReadLine()
				if err != nil {
					result <- "missing quit"
					return
				}
				fmt.Fprint(conn, "221 bye\r\n")
				result <- strings.Join(lines, "\n")
			}()
			sender := &smtpSender{
				address: listener.Addr().String(), host: "127.0.0.1", from: "sender@example.com",
				mode: mode, username: "user", password: "secret",
				tlsConfig: &tls.Config{ServerName: "127.0.0.1", RootCAs: roots, MinVersion: tls.VersionTLS12},
			}
			code, pageURL, subject := "reset-code", "https://game.example/reset-password", "StillWorld password reset"
			send := sender.SendReset
			if mode == "starttls" {
				code, pageURL, subject = "verify-code", "https://game.example/verify-email", "StillWorld email verification"
				send = sender.SendVerification
			}
			if err := send(context.Background(), "player@example.com", code, pageURL); err != nil {
				t.Fatal(err)
			}
			message := <-result
			if !strings.Contains(message, code) || !strings.Contains(message, pageURL) || !strings.Contains(message, subject) || strings.Contains(message, pageURL+"?") {
				t.Fatalf("unexpected message: %q", message)
			}
		})
	}
}

func TestSMTPRejectsMissingSTARTTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "220 test ESMTP\r\n")
		bufio.NewReader(conn).ReadString('\n')
		fmt.Fprint(conn, "250 test\r\n")
	}()
	sender := &smtpSender{address: listener.Addr().String(), host: "127.0.0.1", from: "sender@example.com", mode: "starttls"}
	if err := sender.SendReset(context.Background(), "player@example.com", "code", "https://game.example/reset-password"); err == nil {
		t.Fatal("sent mail without TLS")
	}
}

func TestSMTPCancellationInterruptsGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-done
	}()
	sender := &smtpSender{address: listener.Addr().String(), host: "127.0.0.1", from: "sender@example.com", mode: "starttls"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- sender.SendReset(ctx, "player@example.com", "code", "https://game.example/reset-password")
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP connection not accepted")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled delivery succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt SMTP")
	}
}
