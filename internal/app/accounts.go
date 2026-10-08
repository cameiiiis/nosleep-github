package app

// Password changes, admin-approved recovery, login attempt limits and optional email.

import (
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Password change and admin-approved recovery ---

const resetLifetime = 15 * time.Minute

var errResetCode = inputError("This reset code is invalid or expired. Request a new code.")

type PasswordRequest struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type PasswordResetCode struct {
	ResetCode string    `json:"reset_code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func initAccountSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS password_requests(
		id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL UNIQUE REFERENCES users(id),
		status TEXT NOT NULL CHECK(status IN ('pending','issued','completed','cancelled')),
		created_at INTEGER NOT NULL, token_hash TEXT NOT NULL DEFAULT '', expires_at INTEGER,
		issued_by INTEGER NOT NULL DEFAULT 0);
		CREATE UNIQUE INDEX IF NOT EXISTS password_request_token ON password_requests(token_hash) WHERE token_hash<>'';
		CREATE TABLE IF NOT EXISTS account_attempts(key TEXT PRIMARY KEY, count INTEGER NOT NULL, expires_at INTEGER NOT NULL);
		UPDATE password_requests SET status='pending',token_hash='',expires_at=NULL
		WHERE status='issued' AND issued_by=0;`)
	return err
}

func validateNewPassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return inputError("Choose a password of 8–72 bytes.")
	}
	return nil
}

func (s *Store) ChangePassword(actor User, current, password string) error {
	if err := validateNewPassword(password); err != nil {
		return err
	}
	return s.transact(func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", actor.ID))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return errCredentials
			}
			return err
		}
		if len(current) > 72 || !u.Active || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
			return errCredentials
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := replaceAccountPassword(tx, u.ID, string(hash)); err != nil {
			return err
		}
		return audit(tx, u.ID, "password_changed", u.ID, s.now())
	})
}

func replaceAccountPassword(tx *sql.Tx, userID int64, hash string) error {
	if _, err := tx.Exec("UPDATE users SET password_hash=? WHERE id=? AND active=1", hash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM auth_sessions WHERE user_id=?", userID); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE password_requests SET status='cancelled',token_hash='',expires_at=NULL WHERE user_id=? AND status IN ('pending','issued')", userID)
	return err
}

// A concurrent password change must invalidate a login's previously checked hash.
func (s *Store) SaveVerifiedAuth(tokenHash string, checked User, expires time.Time) error {
	if tokenHash == "" || checked.PasswordHash == "" {
		return errCredentials
	}
	result, err := s.db.Exec(`INSERT INTO auth_sessions(token_hash,user_id,expires_at)
		SELECT ?,id,? FROM users WHERE id=? AND active=1 AND password_hash=?`, tokenHash, stamp(expires), checked.ID, checked.PasswordHash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errCredentials
	}
	return nil
}

// Recovery requests reveal account details only through the authenticated admin list.
func (s *Store) RequestPasswordReset(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.db.Exec(`INSERT INTO password_requests(user_id,status,created_at)
		SELECT id,'pending',? FROM users WHERE email=? AND active=1
		ON CONFLICT(user_id) DO UPDATE SET
		status=CASE WHEN password_requests.status='issued' AND password_requests.expires_at>? THEN 'issued' ELSE 'pending' END,
		token_hash=CASE WHEN password_requests.status='issued' AND password_requests.expires_at>? THEN password_requests.token_hash ELSE '' END,
		expires_at=CASE WHEN password_requests.status='issued' AND password_requests.expires_at>? THEN password_requests.expires_at ELSE NULL END,
		created_at=excluded.created_at`, stamp(s.now()), email, stamp(s.now()), stamp(s.now()), stamp(s.now()))
	return err
}

func (s *Store) PasswordRequests(actor User) ([]PasswordRequest, error) {
	requests := []PasswordRequest{}
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "admin"); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT r.id,u.name,u.email,
		CASE WHEN r.status='issued' AND r.expires_at<=? THEN 'expired' ELSE r.status END,r.created_at
		FROM password_requests r JOIN users u ON u.id=r.user_id WHERE u.active=1 ORDER BY r.created_at DESC,r.id DESC`, stamp(s.now()))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PasswordRequest
			var created int64
			if err := rows.Scan(&r.ID, &r.Name, &r.Email, &r.Status, &created); err != nil {
				return err
			}
			r.CreatedAt = at(created)
			requests = append(requests, r)
		}
		return rows.Err()
	})
	return requests, err
}

func (s *Store) IssuePasswordReset(actor User, requestID int64, verified bool) (PasswordResetCode, error) {
	var code PasswordResetCode
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "admin"); err != nil {
			return err
		}
		if !verified {
			return inputError("Verify the person's identity before issuing a reset code.")
		}
		var err error
		code, err = issuePasswordResetTx(tx, requestID, actor.ID, s.now())
		return err
	})
	return code, err
}

func issuePasswordResetTx(tx *sql.Tx, requestID, actorID int64, now time.Time) (PasswordResetCode, error) {
	if err := require(tx, User{ID: actorID}, "admin"); err != nil {
		return PasswordResetCode{}, err
	}
	code := PasswordResetCode{ResetCode: token(), ExpiresAt: at(stamp(now.Add(resetLifetime)))}
	result, err := tx.Exec(`UPDATE password_requests SET status='issued',token_hash=?,expires_at=?,issued_by=?
		WHERE id=? AND status IN ('pending','issued') AND user_id IN (SELECT id FROM users WHERE active=1)`, hashToken(code.ResetCode), stamp(code.ExpiresAt), actorID, requestID)
	if err != nil {
		return PasswordResetCode{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return PasswordResetCode{}, err
	}
	if n != 1 {
		return PasswordResetCode{}, ErrNotFound
	}
	if err := audit(tx, actorID, "password_reset_issued", requestID, now); err != nil {
		return PasswordResetCode{}, err
	}
	return code, nil
}

func (s *Store) ResetPassword(rawCode, password string) error {
	if err := validateNewPassword(password); err != nil {
		return err
	}
	if len(rawCode) != 64 {
		return errResetCode
	}
	if _, err := hex.DecodeString(rawCode); err != nil {
		return errResetCode
	}
	return s.transact(func(tx *sql.Tx) error {
		var requestID, userID int64
		err := tx.QueryRow(`SELECT r.id,r.user_id FROM password_requests r JOIN users u ON u.id=r.user_id
			WHERE r.token_hash=? AND r.status='issued' AND r.issued_by>0 AND r.expires_at>? AND u.active=1`, hashToken(rawCode), stamp(s.now())).Scan(&requestID, &userID)
		if errors.Is(err, sql.ErrNoRows) {
			return errResetCode
		}
		if err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := replaceAccountPassword(tx, userID, string(hash)); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE password_requests SET status='completed' WHERE id=?", requestID); err != nil {
			return err
		}
		return audit(tx, userID, "password_reset_completed", requestID, s.now())
	})
}

// Persist limits so restarting the local server does not reset an attacker's budget.
func (s *Store) accountAttempt(action, subject, ip string, subjectLimit, ipLimit int) error {
	return s.transact(func(tx *sql.Tx) error {
		now := stamp(s.now())
		if _, err := tx.Exec("DELETE FROM account_attempts WHERE expires_at<=?", now); err != nil {
			return err
		}
		for _, v := range []struct {
			key   string
			limit int
		}{{hashToken(action + "\x00subject\x00" + subject), subjectLimit}, {hashToken(action + "\x00ip\x00" + ip), ipLimit}} {
			var count int
			err := tx.QueryRow("SELECT count FROM account_attempts WHERE key=?", v.key).Scan(&count)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if count >= v.limit {
				return errRateLimited
			}
			if errors.Is(err, sql.ErrNoRows) {
				var size int
				if err := tx.QueryRow("SELECT COUNT(*) FROM account_attempts").Scan(&size); err != nil {
					return err
				}
				if size >= 8192 {
					return errRateLimited
				}
			}
			if _, err := tx.Exec("INSERT INTO account_attempts(key,count,expires_at) VALUES(?,1,?) ON CONFLICT(key) DO UPDATE SET count=count+1", v.key, now+int64(attemptWindow/time.Second)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *api) registerAccountRoutes(mux *http.ServeMux) {
	a.post(mux, "/api/account/password", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		if err := a.store.accountAttempt("change", u.Email, requestIP(r), 5, 100); err != nil {
			return nil, err
		}
		if err := a.store.ChangePassword(u, v.CurrentPassword, v.NewPassword); err != nil {
			return nil, err
		}
		setCookie(w, r, "nosleep_session", "", -1, true)
		return nil, nil
	})
	a.post(mux, "/api/password/forgot", false, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			Email string `json:"email"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		v.Email = strings.ToLower(strings.TrimSpace(v.Email))
		parsed, err := mail.ParseAddress(v.Email)
		if err != nil || parsed.Address != v.Email || len(v.Email) > 254 {
			return nil, inputError("Enter a valid email address.")
		}
		if err := a.store.accountAttempt("forgot", v.Email, requestIP(r), 5, 20); err != nil {
			return nil, err
		}
		if err := a.store.RequestPasswordReset(v.Email); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "message": "If this is an active account, recovery has been requested. An administrator must verify your identity and approve a reset code before you can choose a new password."}, nil
	})
	a.post(mux, "/api/password/reset", false, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			Token       string `json:"token"`
			NewPassword string `json:"new_password"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		if err := a.store.accountAttempt("reset", v.Token, requestIP(r), 5, 20); err != nil {
			return nil, err
		}
		if err := a.store.ResetPassword(v.Token, v.NewPassword); err != nil {
			return nil, err
		}
		setCookie(w, r, "nosleep_session", "", -1, true)
		return nil, nil
	})
	mux.HandleFunc("GET /api/admin/password-requests", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		requests, err := a.store.PasswordRequests(u)
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"requests": requests})
	})
	a.post(mux, "/api/admin/password-reset", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			RequestID        int64 `json:"request_id"`
			IdentityVerified bool  `json:"identity_verified"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		if err := a.store.accountAttempt("issue", u.Email, requestIP(r), 20, 100); err != nil {
			return nil, err
		}
		code, err := a.store.IssuePasswordReset(u, v.RequestID, v.IdentityVerified)
		if err != nil {
			return nil, err
		}
		cfg, configured, err := loadResetSMTP()
		if err != nil {
			log.Print("Password recovery email configuration is incomplete or unsafe; the admin can provide the approved code privately.")
		} else if configured {
			go a.store.deliverPasswordReset(cfg, code)
		}
		return code, nil
	})
}

// --- Limit repeated login and registration attempts ---

const attemptWindow = 15 * time.Minute

type loginWindow struct {
	count   int
	pending int
	until   time.Time
}
type rateLimiter struct {
	mu                           sync.Mutex
	accounts, ips, registrations map[string]loginWindow
	cleanupAt                    time.Time
}

func requestIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
func (l *rateLimiter) prepare(now time.Time) {
	if l.accounts == nil {
		l.accounts = map[string]loginWindow{}
		l.ips = map[string]loginWindow{}
		l.registrations = map[string]loginWindow{}
	}
	if !now.Before(l.cleanupAt) {
		for _, m := range []map[string]loginWindow{l.accounts, l.ips, l.registrations} {
			for key, v := range m {
				if !now.Before(v.until) && v.pending == 0 {
					delete(m, key)
				}
			}
		}
		l.cleanupAt = now.Add(time.Minute)
	}
}
func underLimit(m map[string]loginWindow, key string, limit, capacity int, now time.Time) bool {
	v, exists := m[key]
	if !now.Before(v.until) {
		v.count = 0
	}
	return (!exists && len(m) < capacity) || (exists && v.count+v.pending < limit)
}
func addAttempt(m map[string]loginWindow, key string, capacity int, now time.Time) {
	v, exists := m[key]
	if !exists && len(m) >= capacity {
		return
	}
	if !now.Before(v.until) {
		v = loginWindow{until: now.Add(attemptWindow), pending: v.pending}
	}
	v.count++
	m[key] = v
}
func (l *rateLimiter) allowLogin(email, ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	key := hashToken(email)
	if !underLimit(l.accounts, key, 5, 4096, now) || !underLimit(l.ips, ip, 100, 1024, now) {
		return false
	}
	reserveAttempt(l.accounts, key, now)
	reserveAttempt(l.ips, ip, now)
	return true
}
func reserveAttempt(m map[string]loginWindow, key string, now time.Time) {
	v := m[key]
	if !now.Before(v.until) {
		v.count = 0
		v.until = now.Add(attemptWindow)
	}
	v.pending++
	m[key] = v
}
func releaseAttempt(m map[string]loginWindow, key string, clearFailures bool) {
	v := m[key]
	if v.pending > 0 {
		v.pending--
	}
	if clearFailures {
		v.count = 0
	}
	if v.count == 0 && v.pending == 0 {
		delete(m, key)
	} else {
		m[key] = v
	}
}
func (l *rateLimiter) loginResult(email, ip string, failed, succeeded bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	if failed {
		addAttempt(l.accounts, hashToken(email), 4096, now)
		addAttempt(l.ips, ip, 1024, now)
	}
	releaseAttempt(l.accounts, hashToken(email), succeeded)
	releaseAttempt(l.ips, ip, false)
}
func (l *rateLimiter) allowRegistration(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	if !underLimit(l.registrations, ip, 20, 1024, now) {
		return false
	}
	addAttempt(l.registrations, ip, 1024, now)
	return true
}

// --- Optional email delivery after admin approval ---

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
