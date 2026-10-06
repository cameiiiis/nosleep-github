package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

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
