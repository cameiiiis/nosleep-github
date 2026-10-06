package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestBookingLeadBoundary(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 15, 0, 0, time.UTC)
	start := time.Date(2030, 1, 1, 12, 30, 0, 0, time.UTC)
	if !validSlot(start, 1, now) {
		t.Fatal("A 30-minute slot exactly 15 minutes ahead must be accepted")
	}
	if validSlot(start, 1, now.Add(time.Nanosecond)) {
		t.Fatal("A slot less than 15 minutes ahead by one nanosecond must be rejected")
	}
	if validSlot(start, 1, now.Add(5*time.Minute)) {
		t.Fatal("An aligned future slot only 10 minutes ahead must be rejected")
	}
}

func newBookingLeadFixture(t *testing.T, now *time.Time) (*Store, User, func(string, string, any, int) []byte) {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.Now = func() time.Time { return *now }
	h, err := NewHandler(s, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("NO SLEEP")}}, false)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser("Player", "lead@example.com", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if err := s.SaveAuth(hashToken(token), u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	request := func(method, target string, body any, wantStatus int) []byte {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, target, bytes.NewReader(payload))
		r.AddCookie(&http.Cookie{Name: "nosleep_session", Value: token})
		csrf := strings.Repeat("d", 64)
		r.AddCookie(&http.Cookie{Name: "nosleep_csrf", Value: csrf})
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != wantStatus {
			t.Fatalf("%s %s: status %d, want %d: %s", method, target, w.Code, wantStatus, w.Body.String())
		}
		return w.Body.Bytes()
	}
	return s, u, request
}

func TestBookingLeadHTTPAvailabilityAndCheckout(t *testing.T) {
	base := time.Date(2030, 1, 1, 12, 15, 0, 0, time.UTC)
	start := time.Date(2030, 1, 1, 12, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		now    time.Time
		start  time.Time
		slots  int
		status int
	}{
		{"exact 15-minute threshold", base, start, 1, http.StatusOK},
		{"one nanosecond short", base.Add(time.Nanosecond), start, 1, http.StatusBadRequest},
		{"only ten minutes ahead", base.Add(5 * time.Minute), start, 1, http.StatusBadRequest},
		{"quarter-hour :15 start", base.Add(-15 * time.Minute), start.Add(-15 * time.Minute), 1, http.StatusOK},
		{"quarter-hour :45 start", base, start.Add(15 * time.Minute), 1, http.StatusOK},
		{"reserve two hours ahead", base, base.Add(2 * time.Hour), 2, http.StatusOK},
		{"reserve three hours ahead", base, base.Add(3 * time.Hour), 2, http.StatusOK},
		{"two half-hour slots keep their duration and price", base, start, 2, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, request := newBookingLeadFixture(t, &tc.now)
			query := url.Values{"start": {tc.start.Format(time.RFC3339Nano)}, "slots": {strconv.Itoa(tc.slots)}}
			availability := request("GET", "/api/availability?"+query.Encode(), nil, tc.status)
			checkout := request("POST", "/api/bookings", map[string]any{"pc_id": 1, "start_at": tc.start, "slots": tc.slots, "key": "lead-checkout"}, tc.status)
			if tc.status != http.StatusOK {
				var count int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count); err != nil || count != 0 {
					t.Fatalf("Rejected lead time must not create a booking: count=%d, error=%v", count, err)
				}
				return
			}
			var available struct {
				PCs []PC `json:"pcs"`
			}
			if err := json.Unmarshal(availability, &available); err != nil || len(available.PCs) == 0 {
				t.Fatalf("Threshold availability must include stations: %s, %v", availability, err)
			}
			var booking Booking
			if err := json.Unmarshal(checkout, &booking); err != nil {
				t.Fatal(err)
			}
			if !booking.StartAt.Equal(tc.start) || booking.EndAt.Sub(booking.StartAt) != time.Duration(tc.slots)*30*time.Minute {
				t.Fatalf("Booking must retain 30-minute duration units: %+v", booking)
			}
			snapshot, err := s.Snapshot(actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Items) != 1 || snapshot.Items[0].UnitPesos.String() != "30.00" || snapshot.Items[0].AmountPesos != Pesos(tc.slots)*3000 {
				t.Fatalf("Booking must retain PHP 30 per half-hour slot: %+v", snapshot.Items)
			}
		})
	}
}

func TestBookingLeadIdempotentRetryAfterTimeAdvances(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 15, 0, 0, time.UTC)
	_, _, request := newBookingLeadFixture(t, &now)
	start := now.Add(15 * time.Minute)
	body := map[string]any{"pc_id": 1, "start_at": start, "slots": 1, "key": "lead-retry"}
	var original, retried Booking
	if err := json.Unmarshal(request("POST", "/api/bookings", body, http.StatusOK), &original); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := json.Unmarshal(request("POST", "/api/bookings", body, http.StatusOK), &retried); err != nil {
		t.Fatal(err)
	}
	if retried.ID != original.ID || !retried.StartAt.Equal(original.StartAt) || !retried.EndAt.Equal(original.EndAt) {
		t.Fatalf("Retry after crossing the lead boundary must return the original booking: before=%+v, after=%+v", original, retried)
	}
	request("POST", "/api/bookings", map[string]any{"pc_id": 2, "start_at": start, "slots": 1, "key": "new-too-late"}, http.StatusBadRequest)
}

func TestQuarterHourBookingPartialOverlapAndAdjacency(t *testing.T) {
	for _, tc := range []struct {
		name             string
		existing, wanted time.Duration
	}{
		{"legacy :00 blocks :15", 0, 15 * time.Minute},
		{"quarter :15 blocks :30", 15 * time.Minute, 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			s, actor, _ := newBookingLeadFixture(t, &now)
			base := now.Add(time.Hour)
			original, err := s.CreateBooking(actor, 1, base.Add(tc.existing), 1, "original")
			if err != nil {
				t.Fatal(err)
			}
			start := base.Add(tc.wanted)
			available, err := s.Available(start, start.Add(30*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if bookingLeadContainsPC(available, 1) || !bookingLeadContainsPC(available, 2) {
				t.Fatalf("Partial overlap must exclude only the occupied PC: %+v", available)
			}
			if _, err := s.CreateBooking(actor, 1, start, 1, "overlapping"); !errors.Is(err, ErrConflict) {
				t.Fatalf("Partial overlap must conflict, got %v", err)
			}
			if _, err := s.CreateBooking(actor, 2, start, 1, "other-pc"); err != nil {
				t.Fatalf("The same time on another PC must be allowed: %v", err)
			}
			available, err = s.Available(original.EndAt, original.EndAt.Add(30*time.Minute))
			if err != nil || !bookingLeadContainsPC(available, 1) {
				t.Fatalf("Adjacent time must be available: %+v, %v", available, err)
			}
			adjacent, err := s.CreateBooking(actor, 1, original.EndAt, 1, "adjacent")
			if err != nil || !adjacent.StartAt.Equal(original.EndAt) {
				t.Fatalf("Booking exactly at the prior end must be allowed: %+v, %v", adjacent, err)
			}
		})
	}
}

func bookingLeadContainsPC(pcs []PC, id int64) bool {
	for _, pc := range pcs {
		if pc.ID == id {
			return true
		}
	}
	return false
}

func TestQuarterHourPaidExtensionRespectsPartialOverlap(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, actor, _ := newBookingLeadFixture(t, &now)
	booking, err := s.CreateBooking(actor, 1, now.Add(75*time.Minute), 1, "paid-quarter")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(actor)
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatalf("Expected the base booking item: %+v, %v", snapshot.Items, err)
	}
	cashier, err := s.CreateUser("Cashier", "lead-cashier@example.com", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPayment(cashier, snapshot.Items[0].ID, "cash", "QUARTER-PAY", "quarter-payment"); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.CreateBooking(actor, 1, now.Add(2*time.Hour), 1, "extension-blocker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExtendBooking(actor, booking.ID, 1, "conflicting-extension"); !errors.Is(err, ErrConflict) {
		t.Fatalf("13:45–14:15 extension must conflict with 14:00–14:30 booking: %v", err)
	}
	if err := s.CancelBooking(actor, blocker.ID); err != nil {
		t.Fatal(err)
	}
	extension, err := s.ExtendBooking(actor, booking.ID, 1, "available-extension")
	if err != nil {
		t.Fatal(err)
	}
	if !extension.ExpectedEnd.Equal(booking.EndAt) || extension.ProposedEnd.Sub(extension.ExpectedEnd) != 30*time.Minute || extension.AmountPesos.String() != "30.00" {
		t.Fatalf("Quarter-hour extension must retain 30 minutes for PHP 30: %+v", extension)
	}
	start := now.Add(2 * time.Hour)
	available, err := s.Available(start, start.Add(30*time.Minute))
	if err != nil || bookingLeadContainsPC(available, 1) {
		t.Fatalf("Held 13:45–14:15 extension must block 14:00–14:30 availability: %+v, %v", available, err)
	}
	if _, err := s.CreateBooking(actor, 1, start, 1, "overlaps-extension"); !errors.Is(err, ErrConflict) {
		t.Fatalf("A new booking cannot partially overlap a held extension: %v", err)
	}
}
