package app

import (
	"math"
	"testing"
	"time"
)

func TestNightDiscountRoundingAndMaximumRate(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, "2030-01-02T00:45:59+08:00")
	if got := sessionPrice(3055, start, 1); got != 2750 {
		t.Fatalf("Half-cent final charge must round up: %s", got)
	}
	start, _ = time.Parse(time.RFC3339, "2030-01-02T01:00:00+08:00")
	rate := Pesos(math.MaxInt64 / 16)
	if got := sessionPrice(rate, start, 16); got != rate*14 {
		t.Fatalf("Maximum rate must not overflow: %s", got)
	}
}

func TestNightDiscountBookingBoundariesAndAllTiers(t *testing.T) {
	for _, tc := range []struct {
		name, start string
		pc          int64
		slots       int
		want        Pesos
	}{
		{"Standard", "2030-01-02T01:00:00+08:00", 1, 2, 4800},
		{"Pro", "2030-01-02T01:00:00+08:00", 5, 2, 7200},
		{"Elite", "2030-01-02T01:00:00+08:00", 9, 2, 9600},
		{"before", "2030-01-02T00:00:00+08:00", 1, 2, 6000},
		{"entering", "2030-01-02T00:30:00+08:00", 1, 2, 5400},
		{"quarter entering", "2030-01-02T00:45:00+08:00", 1, 1, 2700},
		{"leaving", "2030-01-02T05:45:00+08:00", 1, 2, 5700},
		{"after", "2030-01-02T06:00:00+08:00", 1, 2, 6000},
		{"eight hours", "2030-01-02T00:45:00+08:00", 1, 16, 42000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, player, _ := cashReferenceFixture(t, ":memory:")
			start, _ := time.Parse(time.RFC3339, tc.start)
			b, err := s.CreateBooking(player, tc.pc, start, tc.slots, "night")
			if err != nil {
				t.Fatal(err)
			}
			var amount Pesos
			if err = s.db.QueryRow("SELECT amount_pesos FROM booking_items WHERE booking_id=?", b.ID).Scan(&amount); err != nil {
				t.Fatal(err)
			}
			if amount != tc.want {
				t.Fatalf("charge = %s; want %s", amount, tc.want)
			}
		})
	}
}

func TestNightDiscountPaymentExtensionAndRefundKeepStoredPrice(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	start, _ := time.Parse(time.RFC3339, "2030-01-02T00:30:00+08:00")
	b, err := s.CreateBooking(player, 1, start, 1, "before-night")
	if err != nil {
		t.Fatal(err)
	}
	var baseID int64
	s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", b.ID).Scan(&baseID)
	if _, err = s.RecordPayment(cashier, baseID, "cash", "", "base-pay"); err != nil {
		t.Fatal(err)
	}
	extra, err := s.ExtendBooking(player, b.ID, 2, "night-extension")
	if err != nil {
		t.Fatal(err)
	}
	if extra.AmountPesos != 4800 {
		t.Fatalf("extension charge = %s", extra.AmountPesos)
	}
	if _, err = s.db.Exec("UPDATE tiers SET price_pesos='90.00' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecordPayment(cashier, extra.ID, "cash", "", "extra-pay")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AmountPesos != 4800 {
		t.Fatalf("stored quote changed: %s", receipt.AmountPesos)
	}
	if err = s.CancelBooking(player, b.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(player)
	if err != nil {
		t.Fatal(err)
	}
	var total Pesos
	for _, r := range snap.Refunds {
		total += r.AmountPesos
	}
	if total != 7800 {
		t.Fatalf("refund must match actual discounted payments: %s", total)
	}
}

func TestNightDiscountImmediateAndScheduledWalkIn(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	now, _ := time.Parse(time.RFC3339, "2030-01-02T02:07:13+08:00")
	s.Now = func() time.Time { return now }
	b, err := s.StartWalkInNowQuoted(cashier, 1, 2, "night-now", "Guest Name", "cash", "", 4800)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Payments) != 1 || snap.Payments[0].AmountPesos != 4800 || !b.StartAt.Equal(now) {
		t.Fatalf("incorrect immediate charge or start: %+v", snap.Payments)
	}
	later, _ := time.Parse(time.RFC3339, "2030-01-02T05:30:00+08:00")
	scheduled, err := s.CreateWalkIn(cashier, 2, later, 2, "night-later", "Later Guest")
	if err != nil {
		t.Fatal(err)
	}
	var amount Pesos
	if err = s.db.QueryRow("SELECT amount_pesos FROM booking_items WHERE booking_id=?", scheduled.ID).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 5400 {
		t.Fatalf("scheduled walk-in charge: %s", amount)
	}
}
