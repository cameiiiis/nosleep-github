package app

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- experience http test ---

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

// --- upgrades http test ---

// Exercise the real routes together: an immediate guest session, reporting,
// backup download, and a later change to the creator's staff permissions.
func TestImmediateWalkInExportsAndRoleChangeHTTP(t *testing.T) {
	now := time.Date(2030, 1, 1, 15, 7, 12, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	setRole := func(role string) {
		t.Helper()
		if _, err := s.db.Exec("UPDATE users SET role=? WHERE id=?", role, actor.ID); err != nil {
			t.Fatal(err)
		}
	}
	body := map[string]any{"guest_name": "Night Guest", "pc_id": 1, "slots": 2, "key": "now-http", "method": "external", "reference": "BANK-ONE", "expected_amount_pesos": "60.00"}
	request("POST", "/api/walk-ins/start-now", body, http.StatusForbidden)
	request("GET", "/api/walk-ins/availability?slots=2", nil, http.StatusForbidden)
	setRole("cashier")
	request("GET", "/api/walk-ins/availability?slots=2", nil, http.StatusOK)
	var booking Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins/start-now", body, http.StatusOK), &booking); err != nil {
		t.Fatal(err)
	}
	if booking.Status != "confirmed" || !booking.StartAt.Equal(now) || !booking.EndAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("Start now must create a confirmed session at server time: %+v", booking)
	}
	now = now.Add(time.Minute)
	var retry Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins/start-now", body, http.StatusOK), &retry); err != nil || retry.ID != booking.ID {
		t.Fatalf("Retry must return the same booking: %+v, %v", retry, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(request("GET", "/api/state", nil, http.StatusOK), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Payments) != 1 || snapshot.Payments[0].AmountPesos.String() != "60.00" || len(snapshot.Receipts) != 1 || len(snapshot.Plays) != 1 || snapshot.Plays[0].EndedAt != nil {
		t.Fatalf("The single verified payment, receipt, and active play must commit together: %+v", snapshot)
	}
	var duplicates struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(request("GET", "/api/payment-reference?reference=bank-one", nil, http.StatusOK), &duplicates); err != nil || duplicates.Count != 1 {
		t.Fatalf("Reference warnings must find the recorded external payment: %+v, %v", duplicates, err)
	}
	data := request("GET", "/api/reports.csv?from=2030-01-01&to=2030-01-01", nil, http.StatusOK)
	if _, err := csv.NewReader(bytes.NewReader(data)).ReadAll(); err != nil {
		t.Fatalf("Report export is not a valid CSV: %v", err)
	}
	for _, text := range []string{"Night Guest", "BANK-ONE", "60.00", booking.Reference} {
		if !strings.Contains(string(data), text) {
			t.Fatalf("Report export must contain %q: %s", text, data)
		}
	}
	request("GET", "/api/admin/backup", nil, http.StatusForbidden)
	setRole("admin")
	backup := request("GET", "/api/admin/backup", nil, http.StatusOK)
	if !bytes.HasPrefix(backup, []byte("SQLite format 3\x00")) {
		t.Fatal("Backup download must be a SQLite database")
	}
	path := filepath.Join(t.TempDir(), "download.sqlite")
	if err := os.WriteFile(path, backup, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var guest string
	if err := db.QueryRow("SELECT guest_name FROM walk_ins WHERE booking_id=?", booking.ID).Scan(&guest); err != nil || guest != "Night Guest" {
		t.Fatalf("The downloaded backup must retain the actual guest session: %q, %v", guest, err)
	}
	setRole("customer")
	var private Snapshot
	if err := json.Unmarshal(request("GET", "/api/state", nil, http.StatusOK), &private); err != nil {
		t.Fatal(err)
	}
	if len(private.Bookings)+len(private.WalkIns)+len(private.Payments)+len(private.Receipts)+len(private.Plays) != 0 {
		t.Fatal("A demoted staff member must no longer receive the guest's booking data")
	}
	request("POST", "/api/extensions", map[string]any{"booking_id": booking.ID, "slots": 1, "key": "demoted-extension"}, http.StatusForbidden)
	request("POST", "/api/refunds", map[string]any{"payment_id": snapshot.Payments[0].ID, "reason": "Guest session"}, http.StatusForbidden)
	request("GET", "/api/reports.csv?from=2030-01-01&to=2030-01-01", nil, http.StatusForbidden)
	request("GET", "/api/payment-reference?reference=BANK-ONE", nil, http.StatusForbidden)
}

func TestMaintenancePublicExplanationAndCashierNamesHTTP(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	request("POST", "/api/bookings", map[string]any{"pc_id": 1, "start_at": now.Add(time.Hour), "slots": 1, "key": "named-player"}, http.StatusOK)
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/admin/maintenance", map[string]any{
		"pc_id": 2, "start_at": now.Add(time.Hour), "end_at": now.Add(2 * time.Hour),
		"reason": "PRIVATE: replacement vendor details", "public_reason": "GPU upgrade in progress",
	}, http.StatusOK)
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	var staff struct {
		Customers []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"customers"`
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(request("GET", "/api/state", nil, http.StatusOK), &staff); err != nil {
		t.Fatal(err)
	}
	if len(staff.Customers) != 1 || staff.Customers[0].ID != actor.ID || staff.Customers[0].Name != actor.Name || len(staff.Users) != 0 {
		t.Fatalf("Cashier must receive booking names without the admin user list: %+v", staff)
	}
	if _, err := s.db.Exec("UPDATE users SET role='customer' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	data := request("GET", "/api/state", nil, http.StatusOK)
	if bytes.Contains(data, []byte("PRIVATE:")) || !bytes.Contains(data, []byte("GPU upgrade in progress")) {
		t.Fatalf("Customer snapshot must contain only the public maintenance explanation: %s", data)
	}
	var customer struct {
		Customers []any `json:"customers"`
	}
	if err := json.Unmarshal(data, &customer); err != nil || len(customer.Customers) != 0 {
		t.Fatalf("Customer must not receive the staff customer-name directory: %+v, %v", customer, err)
	}
}

func TestScheduledCheckInWaitsForActualPreviousPlayToEndHTTP(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	future, err := s.CreateBooking(actor, 1, now.Add(45*time.Minute), 1, "next-reservation")
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err := s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", future.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPayment(actor, itemID, "cash", "", "next-payment"); err != nil {
		t.Fatal(err)
	}
	var immediate Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins/start-now", map[string]any{"pc_id": 1, "slots": 1, "key": "previous-play", "guest_name": "Earlier Guest", "method": "cash", "reference": "", "expected_amount_pesos": "30.00"}, http.StatusOK), &immediate); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Minute)
	request("POST", "/api/play/start", map[string]any{"booking_id": future.ID}, http.StatusConflict)
	var active int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM plays WHERE ended_at IS NULL").Scan(&active); err != nil || active != 1 {
		t.Fatalf("Rejected check-in must leave only the original active play: %d, %v", active, err)
	}
	request("POST", "/api/play/end", map[string]any{"booking_id": immediate.ID}, http.StatusOK)
	request("POST", "/api/play/start", map[string]any{"booking_id": future.ID}, http.StatusOK)
}

func TestImmediatePaymentHTTPRejectsChangedPriceAndKeepsRetryAmount(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"pc_id": 1, "slots": 1, "key": "reviewed-amount", "guest_name": "Reviewed Guest", "method": "cash", "reference": ""}
	request("POST", "/api/walk-ins/start-now", body, http.StatusBadRequest)
	body["expected_amount_pesos"] = "30.00"
	if _, err := s.db.Exec("UPDATE tiers SET price_pesos='60.00' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/walk-ins/start-now", body, http.StatusConflict)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM payments").Scan(&count); err != nil || count != 0 {
		t.Fatalf("Changed price must not record a collection: %d, %v", count, err)
	}
	body["expected_amount_pesos"] = "60.00"
	var booking Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins/start-now", body, http.StatusOK), &booking); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE tiers SET price_pesos='75.00' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var retry Booking
	if err := json.Unmarshal(request("POST", "/api/walk-ins/start-now", body, http.StatusOK), &retry); err != nil || retry.ID != booking.ID {
		t.Fatalf("Retry must honor the already-collected amount: %+v, %v", retry, err)
	}
	var amount Pesos
	if err := s.db.QueryRow("SELECT amount_pesos FROM payments WHERE booking_id=?", booking.ID).Scan(&amount); err != nil || amount.String() != "60.00" {
		t.Fatalf("Recorded amount must match staff's review: %s, %v", amount, err)
	}
}
