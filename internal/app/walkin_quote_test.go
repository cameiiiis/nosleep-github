package app

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

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
