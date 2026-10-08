package app

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

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
