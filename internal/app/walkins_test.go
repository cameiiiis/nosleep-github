package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- walkin test ---

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

// --- walkin now test ---

type immediateWalkInService interface {
	StartWalkInNow(User, int64, int, string, string, string, string) (Booking, error)
}

func immediateService(t *testing.T, s *Store) immediateWalkInService {
	t.Helper()
	service, ok := any(s).(immediateWalkInService)
	if !ok {
		t.Fatal("Store must atomically reserve, collect verified payment, and start an immediate walk-in")
	}
	return service
}

func TestImmediateWalkInAtomicPaymentStartAndRetry(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	svc := immediateService(t, s)
	now := s.now().Add(37*time.Second + 456*time.Millisecond)
	s.Now = func() time.Time { return now }
	b, err := svc.StartWalkInNow(cashier, 5, 2, "now-key", "  Guest One  ", "cash", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "confirmed" || !b.StartAt.Equal(now.Truncate(time.Second)) || !b.EndAt.Equal(b.StartAt.Add(time.Hour)) {
		t.Fatalf("Immediate booking must start on server seconds: %+v", b)
	}
	snap, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Payments) != 1 || len(snap.Receipts) != 1 || len(snap.Plays) != 1 || snap.Items[0].Status != "paid" || snap.Payments[0].AmountPesos.String() != "90.00" || snap.Payments[0].Reference != "CASH-000001" || !snap.Plays[0].StartedAt.Equal(b.StartAt) {
		t.Fatalf("Payment, receipt, and play were not committed together: %+v", snap)
	}
	now = now.Add(2 * time.Hour)
	retry, err := svc.StartWalkInNow(cashier, 5, 2, "now-key", "Guest One", "cash", "another ignored cash reference")
	if err != nil || retry.ID != b.ID || !retry.StartAt.Equal(b.StartAt) {
		t.Fatalf("Retry moved or duplicated immediate start: %+v %v", retry, err)
	}
	snap, err = s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Bookings) != 1 || len(snap.Payments) != 1 || len(snap.Receipts) != 1 || len(snap.Plays) != 1 || len(snap.Audit) != 3 {
		t.Fatalf("Retry duplicated a side effect: %+v", snap)
	}
	for _, tc := range []struct {
		pc                 int64
		slots              int
		guest, method, ref string
	}{{6, 2, "Guest One", "cash", ""}, {5, 1, "Guest One", "cash", ""}, {5, 2, "Other Guest", "cash", ""}, {5, 2, "Guest One", "external", "EXT-1"}} {
		if _, err = svc.StartWalkInNow(cashier, tc.pc, tc.slots, "now-key", tc.guest, tc.method, tc.ref); !errors.Is(err, ErrConflict) {
			t.Fatalf("Changed retry must conflict: %v", err)
		}
	}
	if _, err = s.CreateBooking(cashier, 8, now.Add(time.Hour).Truncate(startStep), 1, "lead-preserved"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateBooking(cashier, 7, now, 1, "online-now"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Online lead rules changed: %v", err)
	}
}

func TestImmediateWalkInRollbackAndAuthoritativeRole(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	svc := immediateService(t, s)
	player.Role = "admin"
	if _, err := svc.StartWalkInNow(player, 1, 1, "forged", "Guest One", "cash", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Forged role accepted: %v", err)
	}
	for _, tc := range []struct {
		slots              int
		guest, method, ref string
	}{{0, "Guest One", "cash", ""}, {17, "Guest One", "cash", ""}, {1, "X", "cash", ""}, {1, "Guest One", "external", ""}, {1, "Guest One", "card", ""}} {
		if _, err := svc.StartWalkInNow(cashier, 1, tc.slots, "invalid", tc.guest, tc.method, tc.ref); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Invalid start accepted: %v", err)
		}
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_play BEFORE INSERT ON plays BEGIN SELECT RAISE(ABORT,'play unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartWalkInNow(cashier, 1, 1, "rollback", "Guest One", "external", "EXT-1"); err == nil {
		t.Fatal("Play failure must abort all writes")
	}
	for _, table := range []string{"bookings", "walk_ins", "booking_items", "booking_slots", "payments", "receipts", "plays", "audit"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("Rollback left %s records: %d %v", table, n, err)
		}
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_play"); err != nil {
		t.Fatal(err)
	}
	b, err := svc.StartWalkInNow(cashier, 1, 1, "rollback", "Guest One", "external", "EXT-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 1, 1, "rollback", "Guest One", "external", "EXT-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("External retry must compare reference: %v", err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='customer' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 1, 1, "rollback", "Guest One", "external", "EXT-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Retry after demotion must be forbidden: %v (booking %d)", err, b.ID)
	}
}

func TestImmediateWalkInWholeDurationAndOvertimeConflicts(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	svc := immediateService(t, s)
	originalNow := s.now()
	future, err := s.CreateBooking(player, 1, originalNow.Add(45*time.Minute), 1, "future")
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err = s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", future.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordPayment(cashier, itemID, "cash", "", "future-pay"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 1, 2, "full-duration", "Guest One", "cash", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Future reservation inside requested duration must conflict: %v", err)
	}
	if _, err = svc.StartWalkInNow(cashier, 1, 1, "fits-before", "Guest One", "cash", ""); err != nil {
		t.Fatalf("Entire non-overlapping duration should fit: %v", err)
	}
	if _, err = s.db.Exec("INSERT INTO maintenance(pc_id,start_at,end_at,reason) VALUES(2,?,?,?)", stamp(originalNow.Add(20*time.Minute)), stamp(originalNow.Add(40*time.Minute)), "Staff note"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 2, 1, "maintenance", "Guest One", "cash", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Maintenance later inside duration must conflict: %v", err)
	}
	if _, err = s.db.Exec("UPDATE pcs SET enabled=0 WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 3, 1, "offline", "Guest One", "cash", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Offline station must conflict: %v", err)
	}
	s.Now = func() time.Time { return originalNow.Add(3 * time.Hour) }
	if _, err = svc.StartWalkInNow(cashier, 1, 1, "overtime", "Guest Two", "cash", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Active overtime play must block new start: %v", err)
	}
	staff, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	var firstID int64
	for _, play := range staff.Plays {
		if play.EndedAt == nil {
			firstID = play.BookingID
		}
	}
	if firstID == 0 {
		t.Fatal("Snapshot must preserve overtime occupancy until staff end it")
	}
	if err = s.EndPlay(cashier, firstID); err != nil {
		t.Fatalf("Staff must be able to end overtime play: %v", err)
	}
	var status string
	var ended int64
	if err = s.db.QueryRow("SELECT b.status,p.ended_at FROM bookings b JOIN plays p ON p.booking_id=b.id WHERE b.id=?", firstID).Scan(&status, &ended); err != nil || status != "completed" || ended != stamp(s.now()) {
		t.Fatalf("Overtime end must use actual staff time: %q %d %v", status, ended, err)
	}
	if _, err = svc.StartWalkInNow(cashier, 1, 1, "after-overtime", "Guest Two", "cash", ""); err != nil {
		t.Fatalf("Ended station must be released: %v", err)
	}
	if _, err = svc.StartWalkInNow(cashier, 4, 1, "free-other", "Guest Two", "external", "EXT-SAME"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartWalkInNow(cashier, 5, 1, "free-second", "Guest Three", "external", "EXT-SAME"); err != nil {
		t.Fatalf("External references are advisory, not unique: %v", err)
	}
}

func TestWalkInAvailabilityReflectsServerDurationAndOvertime(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	now := s.now().Add(17 * time.Second)
	s.Now = func() time.Time { return now }
	player.Role = "cashier"
	if _, err := s.WalkInAvailability(player, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Forged availability role accepted: %v", err)
	}
	if _, err := s.WalkInAvailability(cashier, 17); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Invalid duration accepted: %v", err)
	}
	if _, err := s.StartWalkInNow(cashier, 1, 1, "available-now", "Guest One", "cash", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBooking(player, 2, nextQuarter(now.Add(bookingLead)), 1, "future-availability"); err != nil {
		t.Fatal(err)
	}
	result, err := s.WalkInAvailability(cashier, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.StartAt.Equal(now) || result.Statuses[0].Status != "reserved" || result.Statuses[1].Status != "reserved" {
		t.Fatalf("Duration conflicts or server estimate missing: %+v", result)
	}
	now = now.Add(2 * time.Hour)
	result, err = s.WalkInAvailability(cashier, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Statuses[0].Status != "reserved" || result.Statuses[0].NextStart != nil {
		t.Fatalf("Overtime has no known release time: %+v", result.Statuses[0])
	}
	for _, pc := range result.PCs {
		if pc.ID == 1 {
			t.Fatal("Overtime station leaked into available PCs")
		}
	}
	if _, err = s.db.Exec("UPDATE users SET active=0 WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WalkInAvailability(cashier, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Inactive staff received availability: %v", err)
	}
}

func TestConcurrentImmediateWalkInRetriesCommitOnce(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	var workers sync.WaitGroup
	results := make(chan Booking, 8)
	failures := make(chan error, 8)
	for k := 0; k < 8; k++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			b, err := s.StartWalkInNow(cashier, 1, 1, "concurrent-now", "Guest One", "cash", "")
			if err != nil {
				failures <- err
			} else {
				results <- b
			}
		}()
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("Concurrent same-input retry failed: %v", err)
	}
	var first int64
	for b := range results {
		if first == 0 {
			first = b.ID
		}
		if b.ID != first {
			t.Fatal("Concurrent retries created different bookings")
		}
	}
	snap, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Bookings) != 1 || len(snap.Payments) != 1 || len(snap.Plays) != 1 || len(snap.Audit) != 3 {
		t.Fatalf("Concurrent retries duplicated a financial/play action: %+v", snap)
	}
}

// --- walkin quote test ---

type quotedWalkInStarter interface {
	StartWalkInNowQuoted(User, int64, int, string, string, string, string, Pesos) (Booking, error)
}

func quotedWalkInService(t *testing.T, s *Store) quotedWalkInStarter {
	t.Helper()
	service, ok := any(s).(quotedWalkInStarter)
	if !ok {
		t.Fatal("Immediate start must verify the cashier's quoted amount inside its transaction")
	}
	return service
}

func TestImmediateWalkInRejectsChangedPriceAndRetriesOriginalQuote(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	service := quotedWalkInService(t, s)
	if _, err := s.db.Exec("UPDATE tiers SET price_pesos='60.00' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	b, err := service.StartWalkInNowQuoted(cashier, 1, 2, "quote-now", "Guest Name", "external", "BANK-1", Pesos(6000))
	if !errors.Is(err, ErrConflict) || b.ID != 0 {
		t.Fatalf("A stale PHP60 quote must not collect PHP120: %+v %v", b, err)
	}
	for _, table := range []string{"bookings", "booking_items", "booking_slots", "walk_ins", "payments", "receipts", "plays", "audit"} {
		var count int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Price mismatch left %s records: %d %v", table, count, err)
		}
	}
	b, err = service.StartWalkInNowQuoted(cashier, 1, 2, "quote-now", "Guest Name", "external", "BANK-1", Pesos(12000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE tiers SET price_pesos='75.00' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	retry, err := service.StartWalkInNowQuoted(cashier, 1, 2, "quote-now", "Guest Name", "external", "BANK-1", Pesos(12000))
	if err != nil || retry.ID != b.ID {
		t.Fatalf("Historical successful quote must survive rate changes: %+v %v", retry, err)
	}
	if _, err = service.StartWalkInNowQuoted(cashier, 1, 2, "quote-now", "Guest Name", "external", "BANK-1", Pesos(15000)); !errors.Is(err, ErrConflict) {
		t.Fatalf("Retry cannot change the original verified amount: %v", err)
	}
	snap, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Bookings) != 1 || len(snap.Payments) != 1 || len(snap.Plays) != 1 || len(snap.Audit) != 3 || snap.Payments[0].AmountPesos != Pesos(12000) {
		t.Fatalf("Quote retries changed the financial operation: %+v", snap)
	}
}

func TestImmediateWalkInQuoteRequiredAfterRoleAuthorization(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	service := quotedWalkInService(t, s)
	player.Role = "cashier"
	for _, quote := range []Pesos{0, -1} {
		if _, err := service.StartWalkInNowQuoted(player, 1, 1, "forged-quote", "Guest Name", "cash", "", quote); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Authoritative role must precede quote validation: %v", err)
		}
		if _, err := service.StartWalkInNowQuoted(cashier, 1, 1, "bad-quote", "Guest Name", "cash", "", quote); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Staff cannot verify a non-positive quote: %v", err)
		}
	}
}

func TestImmediateWalkInHTTPRequiresExpectedAmount(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 7, 13, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	body := map[string]any{"pc_id": 1, "slots": 1, "key": "http-quote", "guest_name": "Guest Name", "method": "cash", "reference": ""}
	request("POST", "/api/walk-ins/start-now", body, http.StatusForbidden)
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/walk-ins/start-now", body, http.StatusBadRequest)
	body["expected_amount_pesos"] = 0
	request("POST", "/api/walk-ins/start-now", body, http.StatusBadRequest)
	body["expected_amount_pesos"] = 30
	request("POST", "/api/walk-ins/start-now", body, http.StatusOK)
}
