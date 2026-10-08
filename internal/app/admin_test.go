package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

// --- pc specs test ---

func TestPCSpecsSeedDefaults(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for pass := 0; pass < 2; pass++ {
		if err := s.Seed(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			ids   []int64
			specs string
		}{
			{[]int64{1, 2, 3, 4}, "CPU: Intel Core i5-12400F · RAM: 16 GB · GPU: RTX 4060"},
			{[]int64{5, 6, 7, 8}, "CPU: Intel Core i7-12700F · RAM: 32 GB · GPU: RTX 4070"},
			{[]int64{9, 10, 11, 12}, "CPU: Intel Core i9-13900F · RAM: 32 GB · GPU: RTX 4080"},
		} {
			for _, id := range want.ids {
				var got string
				if err := s.db.QueryRow("SELECT specs FROM pcs WHERE id=?", id).Scan(&got); err != nil || got != want.specs {
					t.Fatalf("Seed pass %d, PC %d specs = %q, want %q, error=%v", pass, id, got, want.specs, err)
				}
			}
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM pcs").Scan(&count); err != nil || count != 12 {
			t.Fatalf("Seeding must retain exactly 12 default stations: %d, %v", count, err)
		}
	}
}

func TestPCSpecsLegacyUpgradePreservesEdits(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`
UPDATE pcs SET specs='RTX 4060 · 16 GB' WHERE id BETWEEN 1 AND 4;
UPDATE pcs SET specs='RTX 4070 · 32 GB' WHERE id BETWEEN 5 AND 8;
UPDATE pcs SET specs='RTX 4080 · 32 GB' WHERE id BETWEEN 9 AND 12;
UPDATE pcs SET code='CUSTOM-02' WHERE id=2;
UPDATE pcs SET tier_id=2 WHERE id=3;
UPDATE pcs SET specs='CPU: Ryzen 7 · RAM: 64 GB · GPU: Radeon' WHERE id=4;
UPDATE pcs SET enabled=0 WHERE id=5;
UPDATE tiers SET price_pesos='39.50' WHERE id=1;
INSERT INTO pcs(id,code,tier_id,specs,enabled) VALUES(13,'PC-13',1,'RTX 4060 · 16 GB',1);`)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := s.Seed(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			id    int64
			specs string
		}{
			{1, "CPU: Intel Core i5-12400F · RAM: 16 GB · GPU: RTX 4060"},
			{2, "RTX 4060 · 16 GB"},
			{3, "RTX 4060 · 16 GB"},
			{4, "CPU: Ryzen 7 · RAM: 64 GB · GPU: Radeon"},
			{5, "CPU: Intel Core i7-12700F · RAM: 32 GB · GPU: RTX 4070"},
			{9, "CPU: Intel Core i9-13900F · RAM: 32 GB · GPU: RTX 4080"},
			{13, "RTX 4060 · 16 GB"},
		} {
			var got string
			if err := s.db.QueryRow("SELECT specs FROM pcs WHERE id=?", want.id).Scan(&got); err != nil || got != want.specs {
				t.Fatalf("Seed pass %d, PC %d specs = %q, want %q, error=%v", pass, want.id, got, want.specs, err)
			}
		}
		var code, price string
		var tierID int64
		var enabled bool
		if err := s.db.QueryRow("SELECT code FROM pcs WHERE id=2").Scan(&code); err != nil || code != "CUSTOM-02" {
			t.Fatalf("Custom station code changed: %q, %v", code, err)
		}
		if err := s.db.QueryRow("SELECT tier_id FROM pcs WHERE id=3").Scan(&tierID); err != nil || tierID != 2 {
			t.Fatalf("Custom station tier changed: %d, %v", tierID, err)
		}
		if err := s.db.QueryRow("SELECT enabled FROM pcs WHERE id=5").Scan(&enabled); err != nil || enabled {
			t.Fatalf("Disabled station became enabled: %v, %v", enabled, err)
		}
		if err := s.db.QueryRow("SELECT price_pesos FROM tiers WHERE id=1").Scan(&price); err != nil || price != "39.50" {
			t.Fatalf("Edited rate changed: %q, %v", price, err)
		}
	}
}

// --- cafe test ---

func cafeFixture(t *testing.T) (*Store, User, User) {
	t.Helper()
	s, p, c := cashReferenceFixture(t, ":memory:")
	if err := initCafeSchema(s.db); err != nil {
		t.Fatal(err)
	}
	return s, p, c
}
func cafeAdmin(t *testing.T, s *Store, c User) User {
	t.Helper()
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	return c
}
func cafeItem(t *testing.T, s *Store, b Booking) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=? AND kind='base'", b.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func cafeBooking(t *testing.T, s *Store, p User, pc int64, key string) Booking {
	t.Helper()
	b, err := s.CreateBooking(p, pc, s.now().Add(time.Hour), 2, key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCafeConfiguredOfferUsesExactRoundingAndPreservesStoredPrice(t *testing.T) {
	s, p, c := cafeFixture(t)
	admin := cafeAdmin(t, s, c)
	offer := NightOffer{Enabled: true, Percent: 35, StartHour: 0, EndHour: 24}
	if err := s.SaveNightOffer(p, offer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("customer offer edit: %v", err)
	}
	if err := s.SaveNightOffer(admin, offer); err != nil {
		t.Fatal(err)
	}
	b := cafeBooking(t, s, p, 1, "configured")
	var amount Pesos
	if err := s.db.QueryRow("SELECT amount_pesos FROM booking_items WHERE booking_id=?", b.ID).Scan(&amount); err != nil || amount != 3900 {
		t.Fatalf("configured amount %s: %v", amount, err)
	}
	offer.Enabled = false
	if err := s.SaveNightOffer(admin, offer); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecordPayment(admin, cafeItem(t, s, b), "cash", "", "pay")
	if err != nil || receipt.AmountPesos != 3900 {
		t.Fatalf("stored quote mutated %+v %v", receipt, err)
	}
	start, _ := time.Parse(time.RFC3339, "2030-01-02T00:45:59+08:00")
	if got := sessionPriceWithOffer(3055, start, 1, NightOffer{true, 20, 1, 6}); got != 2750 {
		t.Fatalf("half cent rounding %s", got)
	}
	for _, bad := range []NightOffer{{true, 81, 1, 6}, {true, -1, 1, 6}, {true, 20, 6, 6}, {true, 20, -1, 6}, {true, 20, 1, 25}} {
		if err := s.SaveNightOffer(admin, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid offer accepted %+v: %v", bad, err)
		}
	}
}

func TestCafeRescheduleTransfersOverlappingAllocationsAndPreservesReceipt(t *testing.T) {
	s, p, c := cafeFixture(t)
	b := cafeBooking(t, s, p, 1, "original")
	receipt, err := s.RecordPayment(c, cafeItem(t, s, b), "external", "BANK", "paid")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestReschedule(p, b.ID, 1, b.StartAt.Add(15*time.Minute), 2, "move")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "second"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pending: %v", err)
	}
	if _, err = s.DecideReschedule(p, req.ID, "approve"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("player approval: %v", err)
	}
	replacement, err := s.DecideReschedule(c, req.ID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Status != "held" || replacement.UserID != p.ID || replacement.ID == b.ID {
		t.Fatalf("replacement %+v", replacement)
	}
	snap, err := s.Snapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Receipts) != 1 || snap.Receipts[0].ID != receipt.ID || len(snap.Refunds) != 1 || snap.Refunds[0].AmountPesos != receipt.AmountPesos {
		t.Fatalf("history/refund lost: %+v", snap)
	}
	var oldStatus string
	var oldSlots, newSlots int
	_ = s.db.QueryRow("SELECT status FROM bookings WHERE id=?", b.ID).Scan(&oldStatus)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE item_id IN(SELECT id FROM booking_items WHERE booking_id=?)", b.ID).Scan(&oldSlots)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE item_id IN(SELECT id FROM booking_items WHERE booking_id=?)", replacement.ID).Scan(&newSlots)
	if oldStatus != "cancelled" || oldSlots != 0 || newSlots != 2 {
		t.Fatalf("allocations old=%s/%d new=%d", oldStatus, oldSlots, newSlots)
	}
}

func TestCafeRescheduleStaleQuoteAndSeatConflictRollback(t *testing.T) {
	for _, kind := range []string{"quote", "seat"} {
		t.Run(kind, func(t *testing.T) {
			s, p, c := cafeFixture(t)
			b := cafeBooking(t, s, p, 1, "old")
			req, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "quote" {
				cafeAdmin(t, s, c)
				if err = s.SaveNightOffer(c, NightOffer{true, 25, 0, 24}); err != nil {
					t.Fatal(err)
				}
			} else {
				cafeBooking(t, s, p, 2, "rival")
			}
			if _, err = s.DecideReschedule(c, req.ID, "approve"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale approval: %v", err)
			}
			var status string
			var count int
			_ = s.db.QueryRow("SELECT status FROM bookings WHERE id=?", b.ID).Scan(&status)
			_ = s.db.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE item_id IN(SELECT id FROM booking_items WHERE booking_id=?)", b.ID).Scan(&count)
			if status != "held" || count != 2 {
				t.Fatalf("rollback lost old booking %s/%d", status, count)
			}
			if _, err = s.DecideReschedule(c, req.ID, "reject"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCafeRescheduleOwnershipAndRefundExclusions(t *testing.T) {
	s, p, c := cafeFixture(t)
	b := cafeBooking(t, s, p, 1, "old")
	other, err := s.CreateUser("Other", "other@local", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(other, b.ID, 2, b.StartAt, 2, "steal"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ownership %v", err)
	}
	receipt, err := s.RecordPayment(c, cafeItem(t, s, b), "cash", "", "pay")
	if err != nil {
		t.Fatal(err)
	}
	refund, err := s.RequestRefund(p, receipt.PaymentID, "requested")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "refund"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending refund %v", err)
	}
	admin := cafeAdmin(t, s, c)
	if err = s.DecideRefund(admin, refund.ID, "reject"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExtendBooking(p, b.ID, 1, "extension"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "extension"); !errors.Is(err, ErrConflict) {
		t.Fatalf("extension %v", err)
	}
}

func TestCafeShiftBoundariesMethodOwnershipAndImmutableClose(t *testing.T) {
	s, p, c := cafeFixture(t)
	before := cafeBooking(t, s, p, 1, "before")
	if _, err := s.RecordPayment(c, cafeItem(t, s, before), "cash", "", "before"); err != nil {
		t.Fatal(err)
	}
	shift, err := s.OpenShift(c, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenShift(c, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate shift %v", err)
	}
	external := cafeBooking(t, s, p, 2, "external")
	r, err := s.RecordPayment(c, cafeItem(t, s, external), "external", "BANK", "external")
	if err != nil {
		t.Fatal(err)
	}
	cash := cafeBooking(t, s, p, 3, "cash")
	if _, err = s.RecordPayment(c, cafeItem(t, s, cash), "cash", "", "cash"); err != nil {
		t.Fatal(err)
	}
	f, err := s.RequestRefund(p, r.PaymentID, "return")
	if err != nil {
		t.Fatal(err)
	}
	admin := cafeAdmin(t, s, c)
	if err = s.DecideRefund(admin, f.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if err = s.DecideRefund(c, f.ID, "complete"); err != nil {
		t.Fatal(err)
	}
	shifts, current, err := s.Shifts(c)
	if err != nil || len(shifts) != 1 || current == nil || current.CashPaymentsPesos != 6000 || current.ExternalPaymentsPesos != 6000 || current.ExternalRefundsPesos != 6000 || current.CashRefundsPesos != 0 {
		t.Fatalf("shift totals %+v %+v %v", shifts, current, err)
	}
	closed, err := s.CloseShift(c, shift.ID, 15999, "counted")
	if err != nil {
		t.Fatal(err)
	}
	if closed.ExpectedCashPesos != 16001 || closed.DifferencePesos == nil || *closed.DifferencePesos != -2 {
		t.Fatalf("cash difference %+v", closed)
	}
	next, err := s.OpenShift(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, current, err = s.Shifts(c)
	if err != nil || current.ID != next.ID || current.CashPaymentsPesos != 0 || current.ExternalRefundsPesos != 0 {
		t.Fatalf("same second doublecount %+v %v", current, err)
	}
	if _, err = s.CloseShift(c, shift.ID, 0, "rewrite"); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed rewrite %v", err)
	}
	if _, _, err = s.Shifts(p); !errors.Is(err, ErrForbidden) {
		t.Fatalf("player shifts %v", err)
	}
	other, err := s.CreateUser("Other cashier", "cashier2@local", "hash")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", other.ID)
	if _, err = s.CloseShift(other, next.ID, 0, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("shift ownership %v", err)
	}
	if err = s.transact(func(tx *sql.Tx) error { _, e := nightOfferTx(tx); return e }); err != nil {
		t.Fatal(err)
	}
}

func TestCafeReschedulePartialCompletedRefundAndHistoryPrivacy(t *testing.T) {
	s, p, c := cafeFixture(t)
	admin := cafeAdmin(t, s, c)
	b := cafeBooking(t, s, p, 1, "old")
	receipt, err := s.RecordPayment(c, cafeItem(t, s, b), "cash", "", "pay")
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.RequestRefund(p, receipt.PaymentID, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE refunds SET amount_pesos='10.55' WHERE id=?", f.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DecideRefund(admin, f.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if err = s.DecideRefund(c, f.ID, "complete"); err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideReschedule(c, req.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	var total Pesos
	for _, refund := range snap.Refunds {
		total += refund.AmountPesos
	}
	if total != receipt.AmountPesos || len(snap.Reschedules) != 1 {
		t.Fatalf("refund overcommit %s/%s or missing history", total, receipt.AmountPesos)
	}
	stranger, err := s.CreateUser("Stranger", "stranger@local", "hash")
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.Snapshot(stranger)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Reschedules) != 0 {
		t.Fatalf("reschedule leak %+v", private.Reschedules)
	}
	guest, err := s.CreateWalkIn(c, 3, b.StartAt, 2, "guest", "Private Guest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(c, guest.ID, 4, b.StartAt, 2, "guest-move"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='customer' WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReschedule(c, guest.ID, 4, b.StartAt, 2, "guest-move"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("demoted walk-in access: %v", err)
	}
}

func TestCafeShiftCashRefundAttributionAndClosedSnapshot(t *testing.T) {
	s, p, c := cafeFixture(t)
	admin := cafeAdmin(t, s, c)
	b := cafeBooking(t, s, p, 1, "before-shift")
	r, err := s.RecordPayment(c, cafeItem(t, s, b), "cash", "", "before")
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.RequestRefund(p, r.PaymentID, "return")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DecideRefund(admin, f.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser("Other", "other-staff@local", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	shift, err := s.OpenShift(other, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DecideRefund(other, f.ID, "complete"); err != nil {
		t.Fatal(err)
	}
	newBooking := cafeBooking(t, s, p, 2, "other-payment")
	if _, err = s.RecordPayment(c, cafeItem(t, s, newBooking), "cash", "", "belongs-to-admin"); err != nil {
		t.Fatal(err)
	}
	closed, err := s.CloseShift(other, shift.ID, 4001, "cash returned")
	if err != nil {
		t.Fatal(err)
	}
	if closed.CashPaymentsPesos != 0 || closed.CashRefundsPesos != 6000 || closed.ExpectedCashPesos != 4001 || *closed.DifferencePesos != 0 {
		t.Fatalf("cash attribution %+v", closed)
	}
	_, err = s.OpenShift(other, 0)
	if err != nil {
		t.Fatal(err)
	}
	later := cafeBooking(t, s, p, 3, "after-close")
	if _, err = s.RecordPayment(other, cafeItem(t, s, later), "cash", "", "new-shift"); err != nil {
		t.Fatal(err)
	}
	shifts, current, err := s.Shifts(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(shifts) != 2 || shifts[1].CashPaymentsPesos != 0 || shifts[1].CashRefundsPesos != 6000 || shifts[1].Notes != "cash returned" || current.CashPaymentsPesos != 6000 {
		t.Fatalf("closed shift changed %+v", shifts)
	}
}

func TestCafeOfferAndShiftRoutesEnforceStoredRoles(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, u, request := newBookingLeadFixture(t, &now)
	request("POST", "/api/admin/discount", map[string]any{"enabled": true, "percent": 35, "start_hour": 0, "end_hour": 24}, 403)
	request("POST", "/api/shifts/open", map[string]any{"opening_cash_pesos": "100.01"}, 403)
	request("GET", "/api/shifts", nil, 403)
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/admin/discount", map[string]any{"enabled": true, "percent": 35, "start_hour": 0, "end_hour": 24}, 200)
	request("POST", "/api/admin/discount", map[string]any{"enabled": true, "percent": 81, "start_hour": 0, "end_hour": 24}, 400)
	var shift CashierShift
	if err := json.Unmarshal(request("POST", "/api/shifts/open", map[string]any{"opening_cash_pesos": "100.01"}, 200), &shift); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/shifts/close", map[string]any{"shift_id": shift.ID, "counted_cash_pesos": "100.00", "notes": "-0.01"}, 200)
	request("GET", "/api/shifts", nil, 200)
}

func TestCafeRescheduleRetryAfterLeadWindowReturnsOriginalProposal(t *testing.T) {
	s, p, _ := cafeFixture(t)
	b := cafeBooking(t, s, p, 1, "old")
	req, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
	if err != nil {
		t.Fatal(err)
	}
	later := b.StartAt.Add(-time.Minute)
	s.Now = func() time.Time { return later }
	retry, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
	if err != nil || retry.ID != req.ID {
		t.Fatalf("retry lost original proposal %+v %v", retry, err)
	}
}

func TestCafeRescheduleOvercommittedRefundRollsBackReplacement(t *testing.T) {
	s, p, c := cafeFixture(t)
	b := cafeBooking(t, s, p, 1, "old")
	receipt, err := s.RecordPayment(c, cafeItem(t, s, b), "cash", "", "pay")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO refunds(payment_id,booking_id,amount_pesos,reason,status,requested_by,created_at,completed_by,completed_at) VALUES(?,?,'60.01','malformed legacy','completed',?,?,?,?)`, receipt.PaymentID, b.ID, p.ID, stamp(s.now()), c.ID, stamp(s.now())); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideReschedule(c, req.ID, "approve"); !errors.Is(err, ErrConflict) {
		t.Fatalf("overcommit approval %v", err)
	}
	var count int
	var status string
	if err = s.db.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT status FROM bookings WHERE id=?", b.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "confirmed" {
		t.Fatalf("partial replacement committed %d %s", count, status)
	}
}

func TestCafeOfferCustomHoursDisabledMaximumAndImmediateQuote(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, "2030-01-02T01:45:00+08:00")
	if got := sessionPriceWithOffer(3055, start, 1, NightOffer{true, 35, 2, 3}); got != 2520 {
		t.Fatalf("custom boundary rounding %s", got)
	}
	if got := sessionPriceWithOffer(3055, start, 1, NightOffer{false, 35, 2, 3}); got != 3055 {
		t.Fatalf("disabled discount %s", got)
	}
	if got := sessionPriceWithOffer(3055, start, 1, NightOffer{true, 0, 0, 24}); got != 3055 {
		t.Fatalf("zero percent %s", got)
	}
	rate := Pesos(math.MaxInt64 / 16)
	if got := sessionPriceWithOffer(rate, start, 16, NightOffer{true, 80, 0, 24}); got <= 0 || got > rate*16 {
		t.Fatalf("maximum overflow %s", got)
	}
	if got := sessionPriceWithOffer(1, start, 1, NightOffer{true, 80, 0, 24}); got != 1 {
		t.Fatalf("minimum positive charge %s", got)
	}
	s, _, c := cafeFixture(t)
	admin := cafeAdmin(t, s, c)
	if err := s.SaveNightOffer(admin, NightOffer{true, 35, 0, 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartWalkInNowQuoted(c, 1, 2, "stale-now", "Guest Name", "cash", "", 6000); !errors.Is(err, ErrConflict) {
		t.Fatalf("old walkin quote %v", err)
	}
	if _, err := s.StartWalkInNowQuoted(c, 1, 2, "configured-now", "Guest Name", "cash", "", 3900); err != nil {
		t.Fatal(err)
	}
}

func TestCafeRescheduleApprovalRechecksPlayRefundAndExtension(t *testing.T) {
	for _, change := range []string{"play", "refund", "extension"} {
		t.Run(change, func(t *testing.T) {
			s, p, c := cafeFixture(t)
			b := cafeBooking(t, s, p, 1, "old")
			receipt, err := s.RecordPayment(c, cafeItem(t, s, b), "cash", "", "pay")
			if err != nil {
				t.Fatal(err)
			}
			req, err := s.RequestReschedule(p, b.ID, 2, b.StartAt, 2, "move")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "play":
				_, err = s.db.Exec("INSERT INTO plays(booking_id,started_at) VALUES(?,?)", b.ID, stamp(s.now()))
			case "refund":
				_, err = s.RequestRefund(p, receipt.PaymentID, "new refund")
			case "extension":
				_, err = s.ExtendBooking(p, b.ID, 1, "new extension")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.DecideReschedule(c, req.ID, "approve"); !errors.Is(err, ErrConflict) {
				t.Fatalf("approval skipped %s guard: %v", change, err)
			}
			var count int
			if err = s.db.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("replacement created on guard failure: %d", count)
			}
		})
	}
}

func TestCafeRescheduleHTTPProposalApprovalAndSnapshot(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, u, request := newBookingLeadFixture(t, &now)
	b := cafeBooking(t, s, u, 1, "old")
	var req RescheduleRequest
	if err := json.Unmarshal(request("POST", "/api/reschedules", map[string]any{"booking_id": b.ID, "pc_id": 2, "start_at": b.StartAt, "slots": 2, "key": "move"}, 200), &req); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/reschedules/action", map[string]any{"reschedule_id": req.ID, "action": "approve"}, 403)
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	var result struct {
		BookingID int64 `json:"booking_id"`
	}
	if err := json.Unmarshal(request("POST", "/api/reschedules/action", map[string]any{"reschedule_id": req.ID, "action": "approve"}, 200), &result); err != nil || result.BookingID <= b.ID {
		t.Fatalf("approval response %+v %v", result, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(request("GET", "/api/state", nil, 200), &snap); err != nil || len(snap.Reschedules) != 1 || snap.Reschedules[0].ReplacementBookingID != result.BookingID || snap.NightOffer.Percent != 20 {
		t.Fatalf("state contracts %+v %v", snap, err)
	}
}

func TestCafeReschedulePublicCheckoutKeyCannotBlockApproval(t *testing.T) {
	s, player, cashier := cafeFixture(t)
	// A normal public checkout may legitimately choose this formerly internal key.
	cafeBooking(t, s, player, 3, "reschedule:1")
	original := cafeBooking(t, s, player, 1, "original")
	proposal, err := s.RequestReschedule(player, original.ID, 2, original.StartAt, 2, "move")
	if err != nil {
		t.Fatal(err)
	}
	if proposal.ID != 1 {
		t.Fatalf("fixture proposal id = %d", proposal.ID)
	}
	replacement, err := s.DecideReschedule(cashier, proposal.ID, "approve")
	if err != nil {
		t.Fatalf("public checkout key blocked legitimate approval: %v", err)
	}
	if replacement.Status != "held" || replacement.PCID != 2 {
		t.Fatalf("replacement = %+v", replacement)
	}
	var key string
	if err = s.db.QueryRow("SELECT checkout_key FROM bookings WHERE id=?", replacement.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key == "reschedule:1" {
		t.Fatal("replacement key remains predictable")
	}
}
