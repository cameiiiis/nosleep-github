package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// --- booking lead test ---

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

// --- cash reference test ---

func cashReferenceFixture(t *testing.T, path string) (*Store, User, User) {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.Now = func() time.Time { return time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC) }
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	player, err := s.CreateUser("Player", "cash-player@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	cashier, err := s.CreateUser("Cashier", "cash-staff@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	return s, player, cashier
}

func cashReferenceItem(t *testing.T, s *Store, player User, pcID int64) int64 {
	t.Helper()
	b, err := s.CreateBooking(player, pcID, s.now().Add(time.Hour), 1, fmt.Sprintf("cash-booking-%d", pcID))
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err := s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", b.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	return itemID
}

func paymentReference(t *testing.T, s *Store, receipt Receipt) string {
	t.Helper()
	var reference string
	if err := s.db.QueryRow("SELECT p.reference FROM payments p JOIN receipts r ON r.payment_id=p.id WHERE r.id=? AND p.id=?", receipt.ID, receipt.PaymentID).Scan(&reference); err != nil {
		t.Fatal(err)
	}
	return reference
}

func TestCashReferencesSequenceAndRetries(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	firstItem := cashReferenceItem(t, s, player, 1)
	first, err := s.RecordPayment(cashier, firstItem, "cash", "", "cash-first")
	if err != nil {
		t.Fatal(err)
	}
	if got := paymentReference(t, s, first); got != "CASH-000001" {
		t.Fatalf("First cash payment reference = %q", got)
	}
	retry, err := s.RecordPayment(cashier, firstItem, "cash", "ignored-client-reference", "cash-first")
	if err != nil || retry.ID != first.ID || retry.Number != first.Number {
		t.Fatalf("Cash retry must return the existing receipt: %+v, %v", retry, err)
	}
	if got := paymentReference(t, s, retry); got != "CASH-000001" {
		t.Fatalf("Cash retry changed the reference to %q", got)
	}
	externalItem := cashReferenceItem(t, s, player, 2)
	if _, err := s.RecordPayment(cashier, externalItem, "external", "  ", "blank-external"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("External payment still requires a reference: %v", err)
	}
	external, err := s.RecordPayment(cashier, externalItem, "external", "  TRANSFER-42  ", "external-payment")
	if err != nil || paymentReference(t, s, external) != "TRANSFER-42" {
		t.Fatalf("External reference must be preserved and trimmed: %+v, %v", external, err)
	}
	if _, err := s.RecordPayment(cashier, externalItem, "external", "DIFFERENT", "external-payment"); !errors.Is(err, ErrConflict) {
		t.Fatalf("External retry cannot change its reference: %v", err)
	}
	if _, err := s.RecordPayment(cashier, 99999, "cash", "", "invalid-item"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Invalid cash payment must not be recorded: %v", err)
	}
	second, err := s.RecordPayment(cashier, cashReferenceItem(t, s, player, 3), "cash", "client-number-is-ignored", "cash-second")
	if err != nil || paymentReference(t, s, second) != "CASH-000002" {
		t.Fatalf("Only recorded cash payments advance the sequence: %+v, %v", second, err)
	}
}

func TestCashReferencesContinueAfterRestartAndLegacyNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cash.db")
	s, player, cashier := cashReferenceFixture(t, path)
	legacy, err := s.RecordPayment(cashier, cashReferenceItem(t, s, player, 1), "cash", "LEGACY-TILL", "legacy-payment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE payments SET reference='CASH-000009' WHERE id=?", legacy.PaymentID); err != nil {
		t.Fatal(err)
	}
	itemID := cashReferenceItem(t, s, player, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reopened.Now = s.Now
	next, err := reopened.RecordPayment(cashier, itemID, "cash", "", "after-restart")
	if err != nil || paymentReference(t, reopened, next) != "CASH-000010" {
		t.Fatalf("Restart must continue past existing generated references: %+v, %v", next, err)
	}
	if paymentReference(t, reopened, legacy) != "CASH-000009" {
		t.Fatal("Recording new cash must not rewrite an existing reference")
	}
}

func TestCashReferencesConcurrentPaymentsAreUnique(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	itemIDs := []int64{cashReferenceItem(t, s, player, 1), cashReferenceItem(t, s, player, 2)}
	type result struct {
		receipt Receipt
		err     error
	}
	results := make(chan result, len(itemIDs))
	for _, itemID := range itemIDs {
		go func(itemID int64) {
			receipt, err := s.RecordPayment(cashier, itemID, "cash", "", fmt.Sprintf("concurrent-%d", itemID))
			results <- result{receipt, err}
		}(itemID)
	}
	var references []string
	for range itemIDs {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		references = append(references, paymentReference(t, s, result.receipt))
	}
	sort.Strings(references)
	if references[0] != "CASH-000001" || references[1] != "CASH-000002" {
		t.Fatalf("Concurrent cash payments must get distinct consecutive references: %v", references)
	}
}

// --- domain privacy test ---

func TestDemotedCashierCannotAccessWalkInAsCustomer(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	start := s.now().Add(time.Hour)
	guest, err := s.CreateWalkIn(cashier, 1, start, 2, "private-guest", "Private Guest")
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.CreateBooking(cashier, 2, start, 1, "own-online")
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err = s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", guest.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecordPayment(cashier, itemID, "cash", "", "guest-pay")
	if err != nil {
		t.Fatal(err)
	}
	extension, err := s.ExtendBooking(cashier, guest.ID, 1, "guest-extension")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestRefund(cashier, receipt.PaymentID, "Private refund"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='customer' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	t.Run("snapshot", func(t *testing.T) {
		snap, err := s.Snapshot(cashier)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Bookings) != 1 || snap.Bookings[0].ID != own.ID || len(snap.WalkIns) != 0 || len(snap.Payments) != 0 || len(snap.Receipts) != 0 || len(snap.Refunds) != 0 {
			t.Fatalf("Guest data leaked to demoted cashier: %+v", snap)
		}
		for _, event := range snap.Audit {
			if event.Action == "payment.recorded" || event.Action == "refund.requested" || event.Action == "extension.held" || event.Action == "booking.created" && event.EntityID == guest.ID {
				t.Fatalf("Guest audit leaked: %+v", event)
			}
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		if err := s.CancelBooking(cashier, guest.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest cancellation must be forbidden: %v", err)
		}
	})
	t.Run("extension retry", func(t *testing.T) {
		if _, err := s.ExtendBooking(cashier, guest.ID, 1, "guest-extension"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest extension retry must be forbidden: %v (item %d)", err, extension.ID)
		}
	})
	t.Run("new extension", func(t *testing.T) {
		if _, err := s.ExtendBooking(cashier, guest.ID, 1, "new-guest-extension"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest extension must be forbidden: %v", err)
		}
	})
	t.Run("refund", func(t *testing.T) {
		if _, err := s.RequestRefund(cashier, receipt.PaymentID, "Again"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest refund must be forbidden: %v", err)
		}
	})
	if err = s.CancelBooking(cashier, own.ID); err != nil {
		t.Fatalf("Demotion must preserve ordinary own booking rights: %v", err)
	}
}

func TestStaffSnapshotHasOnlyBookedCustomerNames(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	if _, err := s.CreateBooking(player, 1, s.now().Add(time.Hour), 1, "customer-name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWalkIn(cashier, 2, s.now().Add(time.Hour), 1, "staff-guest", "Guest Name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("Unbooked", "unused@example.com", "hash"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []User{cashier, player} {
		snap, err := s.Snapshot(actor)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		var names []map[string]any
		if err = json.Unmarshal(data["customers"], &names); err != nil {
			t.Fatalf("Snapshot needs customers array: %v", err)
		}
		if actor.ID == player.ID {
			if len(names) != 0 {
				t.Fatalf("Customer received other names: %+v", names)
			}
			continue
		}
		if len(names) != 1 || len(names[0]) != 2 || names[0]["name"] != player.Name || names[0]["id"] != float64(player.ID) {
			t.Fatalf("Staff needs only booked customer id/name: %+v", names)
		}
		if len(snap.Users) != 0 {
			t.Fatal("Cashier must not get admin user directory")
		}
	}
}

type maintenanceDetailSetter interface {
	SetMaintenanceDetails(User, int64, time.Time, time.Time, string, string) error
}

func TestMaintenancePrivateNotesAndPublicReason(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	start, end := s.now(), s.now().Add(time.Hour)
	if err := s.SetMaintenance(cashier, 1, start, end, "Legacy secret staff note"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(player)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap.Maintenance[0].Reason, "secret") {
		t.Fatal("Legacy maintenance reason leaked to customer")
	}
	setter, ok := any(s).(maintenanceDetailSetter)
	if !ok {
		t.Fatal("Store needs separate private and public maintenance reasons")
	}
	if err = setter.SetMaintenanceDetails(cashier, 2, start, end, "Secret hardware serial", "  Station repair  "); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(player)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snap)
	if snap.Maintenance[1].Reason != "Station repair" || strings.Contains(string(raw), "Secret hardware serial") || strings.Contains(string(raw), "Legacy secret") {
		t.Fatalf("Customer maintenance was not sanitized: %s", raw)
	}
	staff, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(staff)
	if !strings.Contains(string(raw), "Secret hardware serial") || !strings.Contains(string(raw), `"public_reason":"Station repair"`) {
		t.Fatalf("Staff lost maintenance details: %s", raw)
	}
}

func TestMaintenanceLegacyMigrationRetainsPrivateNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-maintenance.db")
	s, player, cashier := cashReferenceFixture(t, path)
	if _, err := s.db.Exec("DROP TABLE maintenance; CREATE TABLE maintenance(id INTEGER PRIMARY KEY,pc_id INTEGER NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,reason TEXT NOT NULL); INSERT INTO maintenance VALUES(9,1,1893499200,1893502800,'Legacy confidential')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reopened, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		var private, public string
		err = reopened.db.QueryRow("SELECT reason,public_reason FROM maintenance WHERE id=9").Scan(&private, &public)
		if err != nil {
			reopened.Close()
			t.Fatal(err)
		}
		if private != "Legacy confidential" || public != "" {
			reopened.Close()
			t.Fatalf("Unsafe migration: private=%q public=%q", private, public)
		}
		customer, err := reopened.Snapshot(player)
		if err != nil {
			t.Fatal(err)
		}
		staff, err := reopened.Snapshot(cashier)
		if err != nil {
			t.Fatal(err)
		}
		if customer.Maintenance[0].Reason != "" || staff.Maintenance[0].Reason != private {
			t.Fatal("Legacy note privacy lost")
		}
		if err = reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
