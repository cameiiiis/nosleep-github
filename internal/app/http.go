package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type api struct {
	store  *Store
	demo   bool
	limits rateLimiter
}
type inputError string

func (e inputError) Error() string { return string(e) }

type bookingAction struct {
	BookingID int64 `json:"booking_id"`
}

var errCredentials = errors.New("Email or password is incorrect, or the account is disabled.")
var errRateLimited = errors.New("Too many attempts. Wait 15 minutes before trying again.")

const dummyHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5V.BzqAeRLpzXOsVyS9V2Vv8p1RkD3e"

func NewHandler(store *Store, assets fs.FS, demo bool) (http.Handler, error) {
	if err := store.Seed(); err != nil {
		return nil, err
	}
	if demo {
		hashes := make([]string, 3)
		for i, p := range []string{"Admin123!", "Cashier123!", "Player123!"} {
			h, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
			if e != nil {
				return nil, e
			}
			hashes[i] = string(h)
		}
		if e := store.SeedDemoUsers(hashes[0], hashes[1], hashes[2]); e != nil {
			return nil, e
		}
	}
	a := &api{store: store, demo: demo}
	mux := http.NewServeMux()
	a.registerOperationsRoutes(mux)
	a.registerWalkInRoutes(mux)
	a.registerToolsRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		csrf := a.csrfCookie(w, r)
		u, e := a.currentUser(r)
		var user any
		if e == nil {
			user = u
		} else if !errors.Is(e, ErrNotFound) {
			a.fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"user": user, "csrf": csrf, "demo": demo})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		u, e := a.currentUser(r)
		if e != nil {
			a.unauthorized(w, e)
			return
		}
		v, e := store.Snapshot(u)
		if e != nil {
			a.fail(w, e)
			return
		}
		reply(w, 200, v)
	})
	mux.HandleFunc("GET /api/availability", func(w http.ResponseWriter, r *http.Request) {
		if _, e := a.currentUser(r); e != nil {
			a.unauthorized(w, e)
			return
		}
		start, e := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
		slots, se := strconv.Atoi(r.URL.Query().Get("slots"))
		if e != nil || se != nil || slots < 1 || slots > 16 {
			a.fail(w, inputError("Choose a valid start time and 1–16 half-hour slots."))
			return
		}
		if !validSlot(start, slots, store.now()) {
			a.fail(w, inputError("Choose a :00, :15, :30, or :45 start at least 15 minutes from now, within the next 30 days."))
			return
		}
		availability, e := store.AvailabilityDetails(start, start.Add(time.Duration(slots)*slot))
		if e != nil {
			a.fail(w, e)
			return
		}
		reply(w, 200, availability)
	})
	a.post(mux, "/api/register", false, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		if !a.limits.allowRegistration(requestIP(r), time.Now()) {
			return nil, errRateLimited
		}
		var v struct {
			Name     string `json:"name"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		v.Name = strings.TrimSpace(v.Name)
		v.Email = strings.ToLower(strings.TrimSpace(v.Email))
		parsed, e := mail.ParseAddress(v.Email)
		if e != nil || parsed.Address != v.Email || len(v.Email) > 254 || utf8.RuneCountInString(v.Name) < 2 || utf8.RuneCountInString(v.Name) > 80 || len(v.Password) < 8 || len(v.Password) > 72 {
			return nil, inputError("Enter a name, valid email, and a password of 8–72 bytes.")
		}
		h, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
		if e != nil {
			return nil, e
		}
		user, e := store.CreateUser(v.Name, v.Email, string(h))
		if e != nil {
			return nil, e
		}
		if e = a.signIn(w, r, user); e != nil {
			return nil, e
		}
		return map[string]any{"user": user}, nil
	})
	a.post(mux, "/api/login", false, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		v.Email = strings.ToLower(strings.TrimSpace(v.Email))
		if !a.limits.allowLogin(v.Email, requestIP(r), time.Now()) {
			return nil, errRateLimited
		}
		failed, succeeded := false, false
		defer func() { a.limits.loginResult(v.Email, requestIP(r), failed, succeeded, time.Now()) }()
		user, e := store.UserByEmail(v.Email)
		if e != nil {
			if !errors.Is(e, ErrNotFound) {
				return nil, e
			}
			_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(v.Password))
			failed = true
			return nil, errCredentials
		}
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(v.Password)) != nil || !user.Active {
			failed = true
			return nil, errCredentials
		}
		succeeded = true
		if e = a.signIn(w, r, user); e != nil {
			return nil, e
		}
		return map[string]any{"user": user}, nil
	})
	a.post(mux, "/api/logout", false, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		if c, e := r.Cookie("nosleep_session"); e == nil {
			if e = store.DeleteAuth(hashToken(c.Value)); e != nil {
				return nil, e
			}
		}
		setCookie(w, r, "nosleep_session", "", -1, true)
		return nil, nil
	})
	a.post(mux, "/api/bookings", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			PCID    int64     `json:"pc_id"`
			StartAt time.Time `json:"start_at"`
			Slots   int       `json:"slots"`
			Key     string    `json:"key"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return store.CreateBooking(u, v.PCID, v.StartAt, v.Slots, v.Key)
	})
	a.post(mux, "/api/payments", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			ItemID    int64  `json:"item_id"`
			Method    string `json:"method"`
			Reference string `json:"reference"`
			Key       string `json:"key"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return store.RecordPayment(u, v.ItemID, v.Method, v.Reference, v.Key)
	})
	a.post(mux, "/api/bookings/cancel", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v bookingAction
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.CancelBooking(u, v.BookingID)
	})
	a.post(mux, "/api/play/start", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v bookingAction
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.StartPlay(u, v.BookingID)
	})
	a.post(mux, "/api/play/end", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v bookingAction
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.EndPlay(u, v.BookingID)
	})
	a.post(mux, "/api/extensions", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			BookingID int64  `json:"booking_id"`
			Slots     int    `json:"slots"`
			Key       string `json:"key"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return store.ExtendBooking(u, v.BookingID, v.Slots, v.Key)
	})
	a.post(mux, "/api/refunds", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			PaymentID int64  `json:"payment_id"`
			Reason    string `json:"reason"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return store.RequestRefund(u, v.PaymentID, v.Reason)
	})
	a.post(mux, "/api/refunds/action", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			RefundID int64  `json:"refund_id"`
			Action   string `json:"action"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.DecideRefund(u, v.RefundID, v.Action)
	})
	a.post(mux, "/api/admin/pcs", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v PC
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.SavePC(u, v)
	})
	a.post(mux, "/api/admin/tiers", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v Tier
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.SaveTier(u, v)
	})
	a.post(mux, "/api/admin/maintenance", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			PCID         int64     `json:"pc_id"`
			StartAt      time.Time `json:"start_at"`
			EndAt        time.Time `json:"end_at"`
			Reason       string    `json:"reason"`
			PublicReason string    `json:"public_reason"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.SetMaintenanceDetails(u, v.PCID, v.StartAt, v.EndAt, v.Reason, v.PublicReason)
	})
	a.post(mux, "/api/admin/users/role", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			UserID int64  `json:"user_id"`
			Role   string `json:"role"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.SetUserRole(u.ID, v.UserID, v.Role)
	})
	a.post(mux, "/api/admin/users/active", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			UserID int64 `json:"user_id"`
			Active bool  `json:"active"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return nil, store.SetUserActive(u.ID, v.UserID, v.Active)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 404, map[string]string{"error": "API route not found."})
	})
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	}), nil
}

func (a *api) post(mux *http.ServeMux, path string, auth bool, action func(http.ResponseWriter, *http.Request, User) (any, error)) {
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		if !validCSRF(r) {
			reply(w, 403, map[string]string{"error": "Your form session expired. Refresh the page and try again."})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			o, e := url.Parse(origin)
			if e != nil || o.Host != r.Host || (o.Scheme != "http" && o.Scheme != "https") {
				reply(w, 403, map[string]string{"error": "Cross-origin requests are not allowed."})
				return
			}
		}
		kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || kind != "application/json" {
			a.fail(w, inputError("Send application/json."))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var user User
		if auth {
			var e error
			user, e = a.currentUser(r)
			if e != nil {
				a.unauthorized(w, e)
				return
			}
		}
		value, e := action(w, r, user)
		if e != nil {
			a.fail(w, e)
			return
		}
		if value == nil {
			value = map[string]bool{"ok": true}
		}
		reply(w, 200, value)
	})
}
func decode(r *http.Request, target any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		return inputError("Invalid form data. Check the fields and try again.")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return inputError("Send one JSON object.")
	}
	return nil
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if e := json.NewEncoder(w).Encode(value); e != nil {
		log.Printf("response encoding: %v", e)
	}
}
func (a *api) fail(w http.ResponseWriter, e error) {
	status := 500
	message := "The request could not be completed. Please try again."
	var input inputError
	switch {
	case errors.As(e, &input), errors.Is(e, ErrInvalid):
		status = 400
		message = e.Error()
	case errors.Is(e, ErrForbidden):
		status = 403
		message = "You do not have permission to perform this action."
	case errors.Is(e, ErrNotFound):
		status = 404
		message = "This record was not found."
	case errors.Is(e, ErrConflict):
		status = 409
		message = e.Error()
	case errors.Is(e, errCredentials):
		status = 401
		message = e.Error()
	case errors.Is(e, errRateLimited):
		status = 429
		message = e.Error()
	}
	if status == 500 {
		log.Printf("request failed: %v", e)
	}
	reply(w, status, map[string]string{"error": message})
}
func (a *api) unauthorized(w http.ResponseWriter, e error) {
	if !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrForbidden) {
		a.fail(w, e)
		return
	}
	reply(w, 401, map[string]string{"error": "Log in to continue."})
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b)
}
func hashToken(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: httpOnly, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (a *api) csrfCookie(w http.ResponseWriter, r *http.Request) string {
	if c, e := r.Cookie("nosleep_csrf"); e == nil && len(c.Value) == 64 {
		return c.Value
	}
	v := token()
	setCookie(w, r, "nosleep_csrf", v, 86400, true)
	return v
}
func validCSRF(r *http.Request) bool {
	c, e := r.Cookie("nosleep_csrf")
	h := r.Header.Get("X-CSRF-Token")
	return e == nil && len(c.Value) == 64 && len(h) == 64 && subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) == 1
}
func (a *api) currentUser(r *http.Request) (User, error) {
	c, e := r.Cookie("nosleep_session")
	if e != nil || len(c.Value) != 64 {
		return User{}, ErrNotFound
	}
	return a.store.AuthUser(hashToken(c.Value))
}
func (a *api) signIn(w http.ResponseWriter, r *http.Request, u User) error {
	if old, e := r.Cookie("nosleep_session"); e == nil {
		if e = a.store.DeleteAuth(hashToken(old.Value)); e != nil {
			return e
		}
	}
	v := token()
	if e := a.store.SaveAuth(hashToken(v), u.ID, time.Now().Add(12*time.Hour)); e != nil {
		return e
	}
	setCookie(w, r, "nosleep_session", v, 12*3600, true)
	return nil
}
