package app

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type resetSMTP struct {
	Addr, Host, User, Password, From, PublicURL string
	ImplicitTLS                                 bool
}

func loadResetSMTP() (resetSMTP, bool, error) {
	cfg := resetSMTP{Addr: strings.TrimSpace(os.Getenv("NO_SLEEP_SMTP_ADDR")), User: os.Getenv("NO_SLEEP_SMTP_USER"), Password: os.Getenv("NO_SLEEP_SMTP_PASSWORD"), From: strings.TrimSpace(os.Getenv("NO_SLEEP_SMTP_FROM")), PublicURL: strings.TrimSpace(os.Getenv("NO_SLEEP_PUBLIC_URL"))}
	if cfg.Addr == "" && cfg.User == "" && cfg.Password == "" && cfg.From == "" && cfg.PublicURL == "" {
		return cfg, false, nil
	}
	invalid := errors.New("invalid password recovery email configuration")
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host == "" || strings.ContainsAny(cfg.Addr, "\r\n\t ") {
		return cfg, false, invalid
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return cfg, false, invalid
	}
	cfg.Host = host
	cfg.ImplicitTLS = n == 465
	if !plainEmail(cfg.From) || (cfg.User == "") != (cfg.Password == "") {
		return cfg, false, invalid
	}
	public, err := url.Parse(cfg.PublicURL)
	if err != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || strings.ContainsAny(cfg.PublicURL, "\r\n") {
		return cfg, false, invalid
	}
	return cfg, true, nil
}

func plainEmail(email string) bool {
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && !strings.ContainsAny(email, "\r\n") && len(email) <= 254
}

func resetMail(cfg resetSMTP, email string, code PasswordResetCode) (string, error) {
	if !plainEmail(cfg.From) || !plainEmail(email) || len(code.ResetCode) != 64 {
		return "", errors.New("invalid reset email")
	}
	public, err := url.Parse(cfg.PublicURL)
	if err != nil || public.Scheme != "https" || public.Host == "" {
		return "", errors.New("invalid recovery URL")
	}
	public.Fragment = "reset=" + code.ResetCode
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: NO SLEEP password recovery\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nA password reset was requested for your NO SLEEP account.\r\n\r\nOpen this link to choose a new password:\r\n%s\r\n\r\nThis link expires at %s and works once. If you did not request this, ignore this email.\r\n", cfg.From, email, public.String(), code.ExpiresAt.Format(time.RFC3339)), nil
}

func sendResetMail(cfg resetSMTP, email string, code PasswordResetCode) error {
	message, err := resetMail(cfg, email, code)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", cfg.Addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.ImplicitTLS {
		secured := tls.Client(conn, tlsConfig)
		if err := secured.Handshake(); err != nil {
			return err
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server must support TLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.User != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return err
	}
	if err := client.Rcpt(email); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(data, message); err != nil {
		data.Close()
		return err
	}
	if err := data.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// Delivery uses an existing admin-approved code; it cannot create or rotate one.
func (s *Store) deliverPasswordReset(cfg resetSMTP, code PasswordResetCode) {
	var email string
	err := s.db.QueryRow(`SELECT u.email FROM password_requests r JOIN users u ON u.id=r.user_id
		WHERE r.token_hash=? AND r.status='issued' AND r.issued_by>0 AND r.expires_at>? AND u.active=1`, hashToken(code.ResetCode), stamp(s.now())).Scan(&email)
	if err != nil {
		return
	}
	if err := sendResetMail(cfg, email, code); err != nil {
		log.Print("Password recovery email delivery failed; the admin can provide the approved code privately.")
	}
}
