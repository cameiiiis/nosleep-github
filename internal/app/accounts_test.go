package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// --- account test ---

func accountFixture(t *testing.T) (*Store, User, User, *time.Time) {
	t.Helper()
	for _, key := range []string{"NO_SLEEP_SMTP_ADDR", "NO_SLEEP_SMTP_USER", "NO_SLEEP_SMTP_PASSWORD", "NO_SLEEP_SMTP_FROM", "NO_SLEEP_PUBLIC_URL"} {
		t.Setenv(key, "")
	}
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := initAccountSchema(s.db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	h, err := bcrypt.GenerateFromPassword([]byte("Original123!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser("Player", "player@example.com", string(h))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.CreateUser("Admin", "admin@example.com", string(h))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", admin.ID); err != nil {
		t.Fatal(err)
	}
	admin.Role = "admin"
	for _, session := range []string{"one", "two"} {
		if err := s.SaveAuth(hashToken(session), u.ID, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return s, u, admin, &now
}

func assertAccountSessionsRevoked(t *testing.T, s *Store, u User) {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM auth_sessions WHERE user_id=?", u.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("all sessions must be revoked: count=%d err=%v", count, err)
	}
}

func TestAccountChangeRequiresCurrentPasswordAndRevokesSessions(t *testing.T) {
	s, u, _, _ := accountFixture(t)
	if err := s.ChangePassword(u, "wrong", "Replacement123!"); !errors.Is(err, errCredentials) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := s.ChangePassword(u, "Original123!", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.ChangePassword(u, "Original123!", strings.Repeat("x", 73)); err == nil {
		t.Fatal("bcrypt truncation length accepted")
	}
	if err := s.ChangePassword(u, "Original123!", "Replacement123!"); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserByID(u.ID)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("Replacement123!")) != nil {
		t.Fatal("password did not change")
	}
	assertAccountSessionsRevoked(t, s, u)
	if _, err := s.db.Exec("UPDATE users SET active=0 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(u, "Replacement123!", "Another123!"); !errors.Is(err, errCredentials) {
		t.Fatalf("inactive change: %v", err)
	}
}

func TestAccountRecoveryVerifiedAdminSingleUseAndHashStorage(t *testing.T) {
	s, u, admin, now := accountFixture(t)
	if err := s.RequestPasswordReset(" PLAYER@example.com "); err != nil {
		t.Fatal(err)
	}
	requests, err := s.PasswordRequests(admin)
	if err != nil || len(requests) != 1 || requests[0].Email != u.Email || requests[0].Status != "pending" {
		t.Fatalf("requests: %+v %v", requests, err)
	}
	if _, err := s.PasswordRequests(u); !errors.Is(err, ErrForbidden) {
		t.Fatalf("customer can list requests: %v", err)
	}
	if _, err := s.IssuePasswordReset(u, requests[0].ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("customer can issue: %v", err)
	}
	if _, err := s.IssuePasswordReset(admin, requests[0].ID, false); err == nil {
		t.Fatal("unverified identity accepted")
	}
	issued, err := s.IssuePasswordReset(admin, requests[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.ResetCode) != 64 || !issued.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("weak code or wrong expiry: %+v", issued)
	}
	var stored string
	if err := s.db.QueryRow("SELECT token_hash FROM password_requests WHERE id=?", requests[0].ID).Scan(&stored); err != nil || stored != hashToken(issued.ResetCode) || stored == issued.ResetCode {
		t.Fatalf("plaintext token stored: %q %v", stored, err)
	}
	if err := s.ResetPassword(issued.ResetCode, "NewPassword123!"); err != nil {
		t.Fatal(err)
	}
	assertAccountSessionsRevoked(t, s, u)
	if err := s.ResetPassword(issued.ResetCode, "NextPassword123!"); err == nil {
		t.Fatal("reused code accepted")
	}
	got, _ := s.UserByID(u.ID)
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("NewPassword123!")) != nil {
		t.Fatal("reset password missing")
	}
}

func TestAccountRecoveryExpiryInactiveAndRotation(t *testing.T) {
	for _, mode := range []string{"expired", "disabled", "rotated", "changed"} {
		t.Run(mode, func(t *testing.T) {
			s, u, admin, now := accountFixture(t)
			if err := s.RequestPasswordReset(u.Email); err != nil {
				t.Fatal(err)
			}
			requests, _ := s.PasswordRequests(admin)
			first, err := s.IssuePasswordReset(admin, requests[0].ID, true)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired":
				*now = now.Add(15 * time.Minute)
			case "disabled":
				if _, err := s.db.Exec("UPDATE users SET active=0 WHERE id=?", u.ID); err != nil {
					t.Fatal(err)
				}
			case "rotated":
				if _, err := s.IssuePasswordReset(admin, requests[0].ID, true); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := s.ChangePassword(u, "Original123!", "Changed123!"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ResetPassword(first.ResetCode, "Replacement123!"); err == nil {
				t.Fatal("invalidated token accepted")
			}
		})
	}
}

func TestAccountRecoveryUnknownEmailCreatesNoRequest(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	if err := s.RequestPasswordReset("missing@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET active=0 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestPasswordReset(u.Email); err != nil {
		t.Fatal(err)
	}
	requests, err := s.PasswordRequests(admin)
	if err != nil || len(requests) != 0 {
		t.Fatalf("unknown/inactive requests: %+v %v", requests, err)
	}
}

func accountHTTP(t *testing.T, s *Store, userID int64) func(string, string, any, int) []byte {
	t.Helper()
	mux := http.NewServeMux()
	a := &api{store: s}
	a.registerAccountRoutes(mux)
	session := hashToken(strconv.FormatInt(userID, 10))
	if userID > 0 {
		if err := s.SaveAuth(hashToken(session), userID, s.now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return func(method, path string, body any, want int) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("Content-Type", "application/json")
		csrf := strings.Repeat("b", 64)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: "nosleep_csrf", Value: csrf})
		if userID > 0 {
			r.AddCookie(&http.Cookie{Name: "nosleep_session", Value: session})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.Bytes()
	}
}

func TestAccountHTTPGenericRecoveryAndBoundedAttempts(t *testing.T) {
	s, _, _, now := accountFixture(t)
	request := accountHTTP(t, s, 0)
	known := request("POST", "/api/password/forgot", map[string]any{"email": "player@example.com"}, 200)
	unknown := request("POST", "/api/password/forgot", map[string]any{"email": "missing@example.com"}, 200)
	if !bytes.Equal(known, unknown) || bytes.Contains(known, []byte("reset_code")) || bytes.Contains(known, []byte("token")) {
		t.Fatalf("enumeration or leaked token: %s / %s", known, unknown)
	}
	for i := 0; i < 4; i++ {
		request("POST", "/api/password/forgot", map[string]any{"email": "player@example.com"}, 200)
	}
	request("POST", "/api/password/forgot", map[string]any{"email": "player@example.com"}, 429)
	*now = now.Add(15 * time.Minute)
	request("POST", "/api/password/forgot", map[string]any{"email": "player@example.com"}, 200)
	for i := 0; i < 5; i++ {
		request("POST", "/api/password/reset", map[string]any{"token": strings.Repeat("c", 64), "new_password": "Replacement123!"}, 400)
	}
	request("POST", "/api/password/reset", map[string]any{"token": strings.Repeat("c", 64), "new_password": "Replacement123!"}, 429)
}

func TestAccountHTTPAuthorizationCSRFAndRevocation(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	anon := accountHTTP(t, s, 0)
	anon("POST", "/api/account/password", map[string]any{"current_password": "Original123!", "new_password": "Replacement123!"}, 401)
	anon("GET", "/api/admin/password-requests", nil, 401)
	customer := accountHTTP(t, s, u.ID)
	customer("GET", "/api/admin/password-requests", nil, 403)
	adminRequest := accountHTTP(t, s, admin.ID)
	adminRequest("GET", "/api/admin/password-requests", nil, 200)
	mux := http.NewServeMux()
	(&api{store: s}).registerAccountRoutes(mux)
	r := httptest.NewRequest("POST", "/api/password/forgot", strings.NewReader(`{"email":"player@example.com"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF missing accepted")
	}
	customer("POST", "/api/account/password", map[string]any{"current_password": "Original123!", "new_password": "Replacement123!"}, 200)
	customer("POST", "/api/account/password", map[string]any{"current_password": "Replacement123!", "new_password": "OtherPassword123!"}, 401)
	assertAccountSessionsRevoked(t, s, u)
}

func TestAccountResetConcurrentConsumptionAndRollback(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	if err := s.RequestPasswordReset(u.Email); err != nil {
		t.Fatal(err)
	}
	requests, _ := s.PasswordRequests(admin)
	code, err := s.IssuePasswordReset(admin, requests[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER prevent_session_delete BEFORE DELETE ON auth_sessions BEGIN SELECT RAISE(ABORT,'blocked'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(code.ResetCode, "Replacement123!"); err == nil {
		t.Fatal("failed session revocation committed password")
	}
	got, _ := s.UserByID(u.ID)
	if bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte("Original123!")) != nil {
		t.Fatal("password was not rolled back")
	}
	if _, err := s.db.Exec("DROP TRIGGER prevent_session_delete"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.ResetPassword(code.ResetCode, "Replacement123!") }()
	}
	success := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, errResetCode) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("single-use concurrent reset: %d successes", success)
	}
	assertAccountSessionsRevoked(t, s, u)
}

func TestAccountAttemptBudgetPersistsAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initAccountSchema(s.db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if err := s.accountAttempt("forgot", "player@example.com", "192.0.2.1", 5, 20); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.Now = func() time.Time { return now }
	if err := s.accountAttempt("forgot", "player@example.com", "192.0.2.2", 5, 20); !errors.Is(err, errRateLimited) {
		t.Fatalf("restart bypassed account limit: %v", err)
	}
	now = now.Add(15 * time.Minute)
	if err := s.accountAttempt("forgot", "player@example.com", "192.0.2.2", 5, 20); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAdminPermissionsAreRechecked(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	if err := s.RequestPasswordReset(u.Email); err != nil {
		t.Fatal(err)
	}
	requests, _ := s.PasswordRequests(admin)
	if _, err := s.db.Exec("UPDATE users SET role='customer' WHERE id=?", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssuePasswordReset(admin, requests[0].ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale admin role accepted: %v", err)
	}
	if _, err := s.PasswordRequests(admin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale admin list accepted: %v", err)
	}
}

func TestAccountStaleLoginCannotCreateSessionAfterPasswordChange(t *testing.T) {
	s, u, _, now := accountFixture(t)
	if err := s.SaveVerifiedAuth(hashToken("verified"), u, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(u, "Original123!", "Replacement123!"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveVerifiedAuth(hashToken("stale-login"), u, now.Add(time.Hour)); !errors.Is(err, errCredentials) {
		t.Fatalf("old password snapshot opened session: %v", err)
	}
	assertAccountSessionsRevoked(t, s, u)
	fresh, err := s.UserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveVerifiedAuth(hashToken("new-login"), fresh, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET active=0 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveVerifiedAuth(hashToken("disabled-login"), fresh, now.Add(time.Hour)); !errors.Is(err, errCredentials) {
		t.Fatalf("disabled account snapshot opened session: %v", err)
	}
}

func TestAccountRecoveryRejectsCodeWithoutAdminApproval(t *testing.T) {
	s, u, admin, now := accountFixture(t)
	if err := s.RequestPasswordReset(u.Email); err != nil {
		t.Fatal(err)
	}
	raw := strings.Repeat("f", 64)
	if _, err := s.db.Exec("UPDATE password_requests SET status='issued',token_hash=?,expires_at=?,issued_by=0", hashToken(raw), stamp(now.Add(resetLifetime))); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(raw, "Replacement123!"); err == nil {
		t.Fatal("An unapproved legacy email code reset the password")
	}
	if err := initAccountSchema(s.db); err != nil {
		t.Fatal(err)
	}
	requests, err := s.PasswordRequests(admin)
	if err != nil || len(requests) != 1 || requests[0].Status != "pending" {
		t.Fatalf("Unapproved recovery was not returned for admin review: %+v %v", requests, err)
	}
	var digest string
	s.db.QueryRow("SELECT token_hash FROM password_requests").Scan(&digest)
	if digest != "" {
		t.Fatal("Unapproved code was not revoked")
	}
	code, err := s.IssuePasswordReset(admin, requests[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResetPassword(code.ResetCode, "Replacement123!"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountHTTPForgotRequiresAdminEvenWithSMTPConfigured(t *testing.T) {
	s, u, admin, _ := accountFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond))
	contacted := make(chan bool, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
		contacted <- err == nil
	}()
	t.Setenv("NO_SLEEP_SMTP_ADDR", listener.Addr().String())
	t.Setenv("NO_SLEEP_SMTP_FROM", "support@example.com")
	t.Setenv("NO_SLEEP_PUBLIC_URL", "https://cafe.example.com/")
	response := accountHTTP(t, s, 0)("POST", "/api/password/forgot", map[string]any{"email": u.Email}, 200)
	if <-contacted {
		t.Fatal("Forgot Password contacted SMTP without admin approval")
	}
	if !bytes.Contains(response, []byte("administrator")) {
		t.Fatalf("Missing admin instructions: %s", response)
	}
	requests, err := s.PasswordRequests(admin)
	if err != nil || len(requests) != 1 || requests[0].Status != "pending" {
		t.Fatalf("Request not pending admin: %+v %v", requests, err)
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM audit WHERE action='password_reset_issued'").Scan(&count)
	if count != 0 {
		t.Fatal("A reset code was issued before approval")
	}
}

// --- auth limits test ---

func TestLoginFailuresAreSeparateFromSharedIPSuccessesAndRegistration(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("Password123!"), bcrypt.MinCost)
	_, err = s.CreateUser("User", "user@example.com", string(hash))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(s, fstest.MapFS{}, false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, email, password, ip string) int {
		payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
		if path == "/api/register" {
			payload = []byte(`{"name":"Registered User","email":"new@example.com","password":"Password123!"}`)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(payload))
		r.RemoteAddr = ip + ":1234"
		csrf := strings.Repeat("a", 64)
		r.AddCookie(&http.Cookie{Name: "nosleep_csrf", Value: csrf})
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 25; i++ {
		if got := request("/api/login", "user@example.com", "Password123!", "192.0.2.1"); got != 200 {
			t.Fatalf("Shared-IP successful sign-in %d returned %d", i+1, got)
		}
	}
	for i := 0; i < 5; i++ {
		if got := request("/api/login", "USER@example.com ", "wrong", fmt.Sprintf("192.0.2.%d", i+2)); got != 401 {
			t.Fatalf("Failure %d: %d", i, got)
		}
	}
	if got := request("/api/login", "user@example.com", "Password123!", "192.0.2.50"); got != 429 {
		t.Fatalf("Account attack from different IPs must throttle: %d", got)
	}
	if got := request("/api/register", "", "", "192.0.2.1"); got != 200 {
		t.Fatalf("Login traffic must not consume registration quota: %d", got)
	}
	for i := 0; i < 100; i++ {
		if got := request("/api/login", fmt.Sprintf("missing%d@example.com", i), "wrong", "192.0.2.99"); got != 401 {
			t.Fatalf("Broad failed-IP attempt %d: %d", i, got)
		}
	}
	if got := request("/api/login", "another@example.com", "wrong", "192.0.2.99"); got != 429 {
		t.Fatalf("Broad failed-IP cap must throttle: %d", got)
	}
}

func TestConcurrentLoginChecksReserveCapacityAndReleaseSuccessfulAttempts(t *testing.T) {
	var limits rateLimiter
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if !limits.allowLogin("target@example.com", fmt.Sprintf("192.0.2.%d", i), now) {
			t.Fatal("Available account capacity rejected")
		}
	}
	if limits.allowLogin("target@example.com", "192.0.2.99", now) {
		t.Fatal("Parallel sign-ins bypassed account throttling before credentials returned")
	}
	for i := 0; i < 5; i++ {
		limits.loginResult("target@example.com", fmt.Sprintf("192.0.2.%d", i), false, true, now)
	}
	for i := 0; i < 100; i++ {
		if !limits.allowLogin(fmt.Sprintf("user%d@example.com", i), "shared-ip", now) {
			t.Fatal("Available IP capacity rejected")
		}
	}
	if limits.allowLogin("different@example.com", "shared-ip", now) {
		t.Fatal("Parallel sign-ins bypassed the broad IP cap")
	}
	for i := 0; i < 100; i++ {
		limits.loginResult(fmt.Sprintf("user%d@example.com", i), "shared-ip", false, true, now)
	}
	if !limits.allowLogin("different@example.com", "shared-ip", now) {
		t.Fatal("Successful concurrent sign-ins consumed failure quota")
	}
	limits.loginResult("different@example.com", "shared-ip", false, false, now)
	for i := 0; i < 5; i++ {
		if !limits.allowLogin("error@example.com", "errors-ip", now) {
			t.Fatal("Store error leaked pending sign-in capacity")
		}
		limits.loginResult("error@example.com", "errors-ip", false, false, now)
	}
}

// --- account smtp test ---

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
