package app

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// --- pesos test ---

func TestPhilippinePesoBookingPaymentRefund(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	var assets fs.FS = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("NO SLEEP")}}
	h, err := NewHandler(s, assets, false)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for i, role := range []string{"customer", "cashier", "admin"} {
		u, err := s.CreateUser(role, role+"@example.com", "test-hash")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE users SET role=? WHERE id=?", role, u.ID); err != nil {
			t.Fatal(err)
		}
		tok := strings.Repeat(string(rune('a'+i)), 64)
		tokens[role] = tok
		if err := s.SaveAuth(hashToken(tok), u.ID, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	request := func(role, method, path string, body any, wantStatus int) map[string]any {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(payload))
		r.AddCookie(&http.Cookie{Name: "nosleep_session", Value: tokens[role]})
		csrf := strings.Repeat("d", 64)
		r.AddCookie(&http.Cookie{Name: "nosleep_csrf", Value: csrf})
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != wantStatus {
			t.Fatalf("%s: status %d, want %d: %s", path, w.Code, wantStatus, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "centavos") {
			t.Fatalf("API exposes old currency unit: %s", w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	state := request("admin", "GET", "/api/state", nil, 200)
	tiers := state["tiers"].([]any)
	if tiers[0].(map[string]any)["price_pesos"] != float64(30) {
		t.Fatalf("Expected PHP 30.00, got %v", tiers[0])
	}
	for _, invalid := range []any{0, -1, 30.555, "not money"} {
		request("admin", "POST", "/api/admin/tiers", map[string]any{"id": 1, "name": "Standard", "price_pesos": invalid}, 400)
	}
	request("admin", "POST", "/api/admin/tiers", map[string]any{"id": 1, "name": "Standard", "price_pesos": 30.55}, 200)
	b := request("customer", "POST", "/api/bookings", map[string]any{"pc_id": 1, "start_at": now.Add(time.Hour), "slots": 4, "key": "peso-booking"}, 200)
	state = request("customer", "GET", "/api/state", nil, 200)
	item := state["items"].([]any)[0].(map[string]any)
	if item["amount_pesos"] != 122.2 {
		t.Fatalf("Four slots should cost PHP 122.20: %v", item)
	}
	request("admin", "POST", "/api/admin/tiers", map[string]any{"id": 1, "name": "Standard", "price_pesos": 40.10}, 200)
	receipt := request("cashier", "POST", "/api/payments", map[string]any{"item_id": item["id"], "method": "cash", "reference": "DEMO", "key": "peso-payment"}, 200)
	if receipt["amount_pesos"] != 122.2 {
		t.Fatalf("Captured quote changed: %v", receipt)
	}
	extension := request("customer", "POST", "/api/extensions", map[string]any{"booking_id": b["id"], "slots": 2, "key": "peso-extension"}, 200)
	if extension["amount_pesos"] != 80.2 {
		t.Fatalf("Extension should cost PHP 80.20: %v", extension)
	}
	request("cashier", "POST", "/api/payments", map[string]any{"item_id": extension["id"], "method": "cash", "reference": "DEMO EXTENSION", "key": "peso-extension-payment"}, 200)
	request("customer", "POST", "/api/refunds", map[string]any{"payment_id": receipt["payment_id"], "reason": "Test peso refund"}, 200)
	request("customer", "POST", "/api/refunds", map[string]any{"payment_id": receipt["payment_id"], "reason": "Duplicate"}, 409)
	request("customer", "POST", "/api/bookings/cancel", map[string]any{"booking_id": b["id"]}, 200)
	state = request("admin", "GET", "/api/state", nil, 200)
	refund := state["refunds"].([]any)[0].(map[string]any)
	if refund["amount_pesos"] != 122.2 {
		t.Fatalf("Wrong refund: %v", refund)
	}
	if len(state["refunds"].([]any)) != 2 {
		t.Fatalf("Expected one refund per payment: %v", state["refunds"])
	}
	for _, entry := range state["refunds"].([]any) {
		id := entry.(map[string]any)["id"]
		request("admin", "POST", "/api/refunds/action", map[string]any{"refund_id": id, "action": "approve"}, 200)
		request("cashier", "POST", "/api/refunds/action", map[string]any{"refund_id": id, "action": "complete"}, 200)
	}
	state = request("admin", "GET", "/api/state", nil, 200)
	report := state["report"].(map[string]any)
	if report["payments_pesos"] != 202.4 || report["refunds_pesos"] != 202.4 || report["net_pesos"] != float64(0) {
		t.Fatalf("Wrong peso report: %v", report)
	}
	var saved string
	if err := s.db.QueryRow("SELECT amount_pesos FROM receipts WHERE id=?", receipt["id"]).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != "122.20" {
		t.Fatalf("Database should store PHP 122.20, got %q", saved)
	}
}

func TestPesoDecimalsAreExact(t *testing.T) {
	for _, value := range []string{"30", "30.5", "30.55", "0.01", "-30.55"} {
		money, err := ParsePesos(value)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		encoded, err := json.Marshal(money)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Pesos
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != money {
			t.Fatalf("Round trip %q: %s, %v", value, encoded, err)
		}
	}
	for _, invalid := range []string{"", "30.555", "NaN", "Infinity", "1e5", "92233720368547758.08", "30..5"} {
		if _, err := ParsePesos(invalid); err == nil {
			t.Fatalf("Accepted invalid peso amount %q", invalid)
		}
	}
	a, _ := ParsePesos("0.10")
	b, _ := ParsePesos("0.20")
	if (a + b).String() != "0.30" {
		t.Fatal("Decimal addition is not exact")
	}
}

// --- night discount test ---

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
