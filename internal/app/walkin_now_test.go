package app

import (
	"errors"
	"sync"
	"testing"
	"time"
)

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
