package app

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

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
