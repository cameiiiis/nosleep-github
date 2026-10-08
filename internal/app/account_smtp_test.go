package app

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAccountSMTPRequiresSafeConfiguration(t *testing.T) {
	accountFixture(t)
	if _, configured, err := loadResetSMTP(); configured || err != nil {
		t.Fatalf("unconfigured local flow: configured=%v err=%v", configured, err)
	}
	t.Setenv("NO_SLEEP_SMTP_ADDR", "mail.example.com:587")
	if _, _, err := loadResetSMTP(); err == nil {
		t.Fatal("incomplete configuration accepted")
	}
	t.Setenv("NO_SLEEP_SMTP_FROM", "recovery@example.com")
	t.Setenv("NO_SLEEP_PUBLIC_URL", "http://cafe.example.com")
	if _, _, err := loadResetSMTP(); err == nil {
		t.Fatal("unencrypted recovery site accepted")
	}
	t.Setenv("NO_SLEEP_PUBLIC_URL", "https://cafe.example.com")
	if _, configured, err := loadResetSMTP(); !configured || err != nil {
		t.Fatalf("valid TLS SMTP configuration: %v %v", configured, err)
	}
	t.Setenv("NO_SLEEP_SMTP_USER", "operator")
	if _, _, err := loadResetSMTP(); err == nil {
		t.Fatal("SMTP username without password accepted")
	}
	t.Setenv("NO_SLEEP_SMTP_PASSWORD", "secret")
	t.Setenv("NO_SLEEP_SMTP_FROM", "recovery@example.com\r\nBcc: other@example.com")
	if _, _, err := loadResetSMTP(); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestAccountSMTPMessageUsesFragmentWithoutHeaderInjection(t *testing.T) {
	cfg := resetSMTP{From: "recovery@example.com", PublicURL: "https://cafe.example.com/"}
	code := PasswordResetCode{ResetCode: strings.Repeat("d", 64), ExpiresAt: time.Date(2030, 1, 1, 12, 15, 0, 0, time.UTC)}
	message, err := resetMail(cfg, "player@example.com", code)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "https://cafe.example.com/#reset="+code.ResetCode) || strings.Contains(message, "?token=") {
		t.Fatalf("reset token must travel in URL fragment: %s", message)
	}
	if _, err := resetMail(cfg, "player@example.com\r\nBcc: other@example.com", code); err == nil {
		t.Fatal("recipient header injection accepted")
	}
}

func TestAccountSMTPRefusesPlaintextBeforeCredentialsOrToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			commands <- ""
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		conn.Write([]byte("220 local test SMTP\r\n"))
		reader := bufio.NewReader(conn)
		command, _ := reader.ReadString('\n')
		conn.Write([]byte("250 local test SMTP\r\n"))
		for {
			line, err := reader.ReadString('\n')
			command += line
			if err != nil {
				break
			}
		}
		commands <- command
	}()
	code := PasswordResetCode{ResetCode: strings.Repeat("e", 64), ExpiresAt: time.Now().Add(15 * time.Minute)}
	cfg := resetSMTP{Addr: listener.Addr().String(), Host: "127.0.0.1", From: "recovery@example.com", PublicURL: "https://cafe.example.com", User: "operator", Password: "do-not-transmit"}
	if err := sendResetMail(cfg, "player@example.com", code); err == nil {
		t.Fatal("plaintext SMTP accepted")
	}
	command := <-commands
	if !strings.Contains(command, "EHLO") || strings.Contains(command, "AUTH") || strings.Contains(command, "MAIL") || strings.Contains(command, code.ResetCode) || strings.Contains(command, cfg.Password) {
		t.Fatalf("plaintext connection received credentials or message: %q", command)
	}
}

func TestAccountSMTPFailurePreservesAdminApprovedCode(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	if err := s.RequestPasswordReset(u.Email); err != nil {
		t.Fatal(err)
	}
	requests, _ := s.PasswordRequests(admin)
	code, err := s.IssuePasswordReset(admin, requests[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// Port zero is never a delivery service; this exercises failure without email.
	s.deliverPasswordReset(resetSMTP{Addr: "127.0.0.1:0", Host: "127.0.0.1", From: "recovery@example.com", PublicURL: "https://cafe.example.com"}, code)
	requests, err = s.PasswordRequests(admin)
	if err != nil || len(requests) != 1 || requests[0].Status != "issued" {
		t.Fatalf("SMTP failure recovery status: %+v %v", requests, err)
	}
	var digest string
	if err := s.db.QueryRow("SELECT token_hash FROM password_requests WHERE id=?", requests[0].ID).Scan(&digest); err != nil || digest != hashToken(code.ResetCode) {
		t.Fatalf("admin-approved fallback code lost: %v", err)
	}
	if err = s.ResetPassword(code.ResetCode, "Replacement123!"); err != nil {
		t.Fatal(err)
	}
}
