package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type walkInCreator interface {
	CreateWalkIn(User, int64, time.Time, int, string, string) (Booking, error)
}

func walkInService(t *testing.T, s *Store) walkInCreator {
	t.Helper()
	service, ok := any(s).(walkInCreator)
	if !ok {
		t.Fatal("Store must support staff bookings for named walk-in guests")
	}
	return service
}

func TestWalkInHoldPaymentAndIdempotency(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	service := walkInService(t, s)
	start := s.now().Add(3 * time.Hour)
	b, err := service.CreateWalkIn(cashier, 5, start, 2, "walkin-1", "  Juan Dela Cruz  ")
	if err != nil {
		t.Fatal(err)
	}
	if b.UserID != cashier.ID || b.Status != "held" || !b.HoldUntil.Equal(s.now().Add(10*time.Minute)) || !b.EndAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("Walk-in must retain the normal staff-owned ten-minute hold: %+v", b)
	}
	var guest string
	var itemID int64
	var amount Pesos
	if err := s.db.QueryRow("SELECT guest_name FROM walk_ins WHERE booking_id=?", b.ID).Scan(&guest); err != nil || guest != "Juan Dela Cruz" {
		t.Fatalf("Guest name must be persisted and trimmed: %q, %v", guest, err)
	}
	if err := s.db.QueryRow("SELECT id,amount_pesos FROM booking_items WHERE booking_id=?", b.ID).Scan(&itemID, &amount); err != nil || amount.String() != "90.00" {
		t.Fatalf("Walk-in must use the selected tier rate: %v, %v", amount, err)
	}
	// Retry after the lead boundary must return the same hold, never another booking.
	s.Now = func() time.Time { return start.Add(-10 * time.Minute) }
	retry, err := service.CreateWalkIn(cashier, 5, start, 2, "walkin-1", "Juan Dela Cruz")
	if err != nil || retry.ID != b.ID {
		t.Fatalf("Retry must reuse the existing booking: %+v, %v", retry, err)
	}
	for _, attempt := range []struct {
		pc    int64
		slots int
		name  string
	}{
		{5, 2, "Different guest"}, {6, 2, "Juan Dela Cruz"}, {5, 1, "Juan Dela Cruz"},
	} {
		if _, err := service.CreateWalkIn(cashier, attempt.pc, start, attempt.slots, "walkin-1", attempt.name); !errors.Is(err, ErrConflict) {
			t.Fatalf("A retry cannot change the guest or booked details: %v", err)
		}
	}
	if _, err := s.CreateBooking(cashier, 5, start, 2, "walkin-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Normal checkout cannot reuse a walk-in key: %v", err)
	}
	// Use a fresh valid hold to verify payment remains a separate staff action.
	s.Now = func() time.Time { return time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC) }
	fresh, err := service.CreateWalkIn(cashier, 6, start, 2, "walkin-payment", "Maria Santos")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", fresh.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPayment(cashier, itemID, "cash", "", "walkin-paid"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow("SELECT status FROM bookings WHERE id=?", fresh.ID).Scan(&status); err != nil || status != "confirmed" {
		t.Fatalf("Verified payment must confirm the walk-in: %q, %v", status, err)
	}
	if _, err := s.CreateBooking(cashier, 7, start, 1, "staff-normal-booking"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWalkIn(cashier, 7, start, 1, "staff-normal-booking", "Juan Cruz"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Walk-in checkout cannot reuse a normal booking key: %v", err)
	}
}

func TestWalkInAuthorizationValidationAndRollback(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	service := walkInService(t, s)
	start := s.now().Add(time.Hour)
	player.Role = "admin"
	if _, err := service.CreateWalkIn(player, 1, start, 1, "forged-staff", "Juan Cruz"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Customer cannot create a walk-in using a forged role: %v", err)
	}
	if _, err := s.db.Exec("UPDATE users SET active=0 WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWalkIn(cashier, 1, start, 1, "inactive", "Juan Cruz"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Inactive staff cannot create a walk-in: %v", err)
	}
	if _, err := s.db.Exec("UPDATE users SET active=1 WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", " X ", strings.Repeat("界", 81)} {
		if _, err := service.CreateWalkIn(cashier, 1, start, 1, "bad-name", name); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Invalid guest name must be rejected: %v", err)
		}
	}
	if _, err := service.CreateWalkIn(cashier, 1, s.now().Add(10*time.Minute), 1, "too-soon", "Juan Cruz"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Walk-in must obey the existing lead and quarter-hour rules: %v", err)
	}
	if _, err := s.CreateBooking(player, 1, start, 1, "normal-checkout"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWalkIn(cashier, 1, start, 1, "conflict", "Juan Cruz"); !errors.Is(err, ErrConflict) {
		t.Fatalf("A reserved station cannot be double-booked: %v", err)
	}
	if _, err := service.CreateWalkIn(player, 1, start, 1, "normal-checkout", "Juan Cruz"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Walk-in retry must still authorize staff: %v", err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER reject_guest BEFORE INSERT ON walk_ins BEGIN SELECT RAISE(ABORT, 'guest rejected'); END;"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWalkIn(cashier, 2, start, 1, "guest-rollback", "Juan Cruz"); err == nil {
		t.Fatal("Guest insert failure must fail the complete operation")
	}
	var bookings, items, slots, guests int
	if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM bookings),(SELECT COUNT(*) FROM booking_items),(SELECT COUNT(*) FROM booking_slots),(SELECT COUNT(*) FROM walk_ins)").Scan(&bookings, &items, &slots, &guests); err != nil {
		t.Fatal(err)
	}
	if bookings != 1 || items != 1 || slots != 1 || guests != 0 {
		t.Fatalf("Failed walk-ins left partial records: bookings=%d items=%d slots=%d guests=%d", bookings, items, slots, guests)
	}
}

func TestWalkInSnapshotPrivacyAndExistingDatabaseStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	s, player, cashier := cashReferenceFixture(t, path)
	service := walkInService(t, s)
	b, err := service.CreateWalkIn(cashier, 1, s.now().Add(time.Hour), 1, "visible-walkin", "Juan Cruz")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		actor User
		count int
	}{{cashier, 1}, {player, 0}} {
		snapshot, err := s.Snapshot(test.actor)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &data); err != nil {
			t.Fatal(err)
		}
		var guests []struct {
			BookingID int64  `json:"booking_id"`
			GuestName string `json:"guest_name"`
		}
		if err := json.Unmarshal(data["walk_ins"], &guests); err != nil {
			t.Fatalf("Snapshot must include a walk_ins array: %v", err)
		}
		if len(guests) != test.count {
			t.Fatalf("Snapshot exposed %d guests to %s; expected %d", len(guests), test.actor.Name, test.count)
		}
		if test.count == 1 && (guests[0].BookingID != b.ID || guests[0].GuestName != "Juan Cruz") {
			t.Fatalf("Snapshot lost guest identity: %+v", guests)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	persisted, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var guest string
	if err := persisted.db.QueryRow("SELECT guest_name FROM walk_ins WHERE booking_id=?", b.ID).Scan(&guest); err != nil || guest != "Juan Cruz" {
		persisted.Close()
		t.Fatalf("Guest name must survive a database restart: %q, %v", guest, err)
	}
	if _, err := persisted.db.Exec("DROP TABLE walk_ins"); err != nil {
		persisted.Close()
		t.Fatal(err)
	}
	if err := persisted.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM walk_ins").Scan(&count); err != nil || count != 0 {
		t.Fatalf("Startup must add the guest table to existing databases: %d, %v", count, err)
	}
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count); err != nil || count != 1 {
		t.Fatalf("Startup must preserve old booking records: %d, %v", count, err)
	}
}
