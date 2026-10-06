package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// The HTTP workflow connects guest reservations, availability, payment, and day-end reports.
func TestWalkInPaymentAndPhilippineDayReportsHTTP(t *testing.T) {
	now := time.Date(2030, 1, 1, 15, 0, 0, 0, time.UTC) // 11 PM in Manila.
	s, actor, request := newBookingLeadFixture(t, &now)
	setRole := func(role string) {
		t.Helper()
		if _, err := s.db.Exec("UPDATE users SET role=? WHERE id=?", role, actor.ID); err != nil {
			t.Fatal(err)
		}
	}
	start := now.Add(45 * time.Minute)
	body := map[string]any{"guest_name": "  Midnight Guest  ", "pc_id": 1, "start_at": start, "slots": 2, "key": "walk-in-http"}
	request("POST", "/api/walk-ins", body, http.StatusForbidden)
	request("GET", "/api/reports?from=2030-01-01&to=2030-01-01", nil, http.StatusForbidden)
	setRole("cashier")
	var booking Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins", body, http.StatusOK), &booking); err != nil {
		t.Fatal(err)
	}
	var retry Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins", body, http.StatusOK), &retry); err != nil || retry.ID != booking.ID {
		t.Fatalf("Walk-in retry created a different reservation: %+v, %v", retry, err)
	}
	var snapshot struct {
		WalkIns []struct {
			BookingID int64  `json:"booking_id"`
			GuestName string `json:"guest_name"`
		} `json:"walk_ins"`
		Items []BookingItem `json:"items"`
	}
	if err := json.Unmarshal(request("GET", "/api/state", nil, http.StatusOK), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.WalkIns) != 1 || snapshot.WalkIns[0].BookingID != booking.ID || snapshot.WalkIns[0].GuestName != "Midnight Guest" || len(snapshot.Items) != 1 {
		t.Fatalf("Held walk-in and its guest name must appear for staff: %+v", snapshot)
	}
	query := url.Values{"start": {start.Format(time.RFC3339)}, "slots": {"2"}}
	var availability struct {
		PCs      []PC `json:"pcs"`
		Statuses []struct {
			PCID      int64      `json:"pc_id"`
			Status    string     `json:"status"`
			NextStart *time.Time `json:"next_start"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(request("GET", "/api/availability?"+query.Encode(), nil, http.StatusOK), &availability); err != nil {
		t.Fatal(err)
	}
	for _, pc := range availability.PCs {
		if pc.ID == 1 {
			t.Fatal("A held walk-in must block that PC for the entire session")
		}
	}
	if len(availability.Statuses) != 12 || availability.Statuses[0].Status != "reserved" || availability.Statuses[0].NextStart == nil || !availability.Statuses[0].NextStart.Equal(booking.EndAt) {
		t.Fatalf("Availability must explain the reservation and suggest the next full session: %+v", availability.Statuses)
	}
	var receipt Receipt
	if err := json.Unmarshal(request("POST", "/api/payments", map[string]any{"item_id": snapshot.Items[0].ID, "method": "cash", "reference": "", "key": "walk-in-paid"}, http.StatusOK), &receipt); err != nil || receipt.AmountPesos.String() != "60.00" {
		t.Fatalf("Walk-in must use the ordinary exact-peso payment path: %+v, %v", receipt, err)
	}
	request("POST", "/api/bookings/cancel", map[string]any{"booking_id": booking.ID}, http.StatusOK)
	var refundID int64
	if err := s.db.QueryRow("SELECT id FROM refunds WHERE booking_id=?", booking.ID).Scan(&refundID); err != nil {
		t.Fatal(err)
	}
	setRole("admin")
	request("POST", "/api/refunds/action", map[string]any{"refund_id": refundID, "action": "approve"}, http.StatusOK)
	now = now.Add(65 * time.Minute) // Refund returned after Philippine midnight.
	if err := s.SaveAuth(hashToken("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), actor.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/refunds/action", map[string]any{"refund_id": refundID, "action": "complete"}, http.StatusOK)
	for _, want := range []struct {
		date, payments, refunds, net string
		paymentCount, refundCount    int
	}{
		{"2030-01-01", "60.00", "0.00", "60.00", 1, 0},
		{"2030-01-02", "0.00", "60.00", "-60.00", 0, 1},
	} {
		var result struct {
			Report   Report    `json:"report"`
			Payments []Payment `json:"payments"`
			Refunds  []Refund  `json:"refunds"`
		}
		if err := json.Unmarshal(request("GET", "/api/reports?from="+want.date+"&to="+want.date, nil, http.StatusOK), &result); err != nil {
			t.Fatal(err)
		}
		if result.Report.PaymentsPesos.String() != want.payments || result.Report.RefundsPesos.String() != want.refunds || result.Report.NetPesos.String() != want.net || len(result.Payments) != want.paymentCount || len(result.Refunds) != want.refundCount {
			t.Fatalf("Report for %s must use payment recording/refund completion dates in Manila: %+v", want.date, result)
		}
	}
}
