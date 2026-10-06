package app

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAvailabilityReasonsAndWholeDurationSuggestions(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	start := now.Add(30 * time.Minute)
	if _, err := s.CreateBooking(actor, 1, start, 1, "reason-reserved"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		pc         int64
		start, end time.Time
	}{{1, now.Add(110 * time.Minute), now.Add(130 * time.Minute)}, {2, now.Add(50 * time.Minute), now.Add(65 * time.Minute)}} {
		if _, err := s.db.Exec("INSERT INTO maintenance(pc_id,start_at,end_at,reason) VALUES(?,?,?,?)", m.pc, stamp(m.start), stamp(m.end), "Private staff note"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE pcs SET enabled=0 WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CreateBooking(actor, 4, start, 2, "reason-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelBooking(actor, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := s.CreateBooking(actor, 5, start, 2, "reason-expired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE bookings SET hold_until=? WHERE id=?", stamp(now), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE booking_items SET hold_until=? WHERE booking_id=?", stamp(now), expired.ID); err != nil {
		t.Fatal(err)
	}
	wanted := now.Add(45 * time.Minute)
	query := url.Values{"start": {wanted.Format(time.RFC3339)}, "slots": {"2"}}
	raw := request("GET", "/api/availability?"+query.Encode(), nil, 200)
	var response struct {
		PCs      []PC `json:"pcs"`
		Statuses []struct {
			PCID      int64      `json:"pc_id"`
			Status    string     `json:"status"`
			NextStart *time.Time `json:"next_start"`
		} `json:"statuses"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Statuses) != 12 {
		t.Fatalf("Need status for every station: %s", raw)
	}
	for _, tc := range []struct {
		pc     int64
		status string
		next   time.Time
	}{{1, "reserved", now.Add(135 * time.Minute)}, {2, "maintenance", now.Add(75 * time.Minute)}, {3, "offline", time.Time{}}, {4, "available", time.Time{}}, {5, "available", time.Time{}}} {
		got := response.Statuses[tc.pc-1]
		if got.PCID != tc.pc || got.Status != tc.status {
			t.Fatalf("PC%d: %+v", tc.pc, got)
		}
		if tc.next.IsZero() {
			if got.NextStart != nil {
				t.Fatalf("PC%d unexpected suggestion: %+v", tc.pc, got)
			}
		} else if got.NextStart == nil || !got.NextStart.Equal(tc.next) {
			t.Fatalf("PC%d expected %s, got %+v", tc.pc, tc.next, got)
		}
	}
	for _, p := range response.PCs {
		if p.ID < 4 {
			t.Fatalf("Unavailable station leaked into compatible pcs list: %+v", p)
		}
	}
	if strings.Contains(string(raw), "Private staff note") {
		t.Fatal("Availability must not expose maintenance staff notes")
	}
	// Exact adjacency remains available despite the quarter-hour start of another reservation.
	adjacent := url.Values{"start": {now.Add(time.Hour).Format(time.RFC3339)}, "slots": {"1"}}
	var adjacentResponse struct {
		Statuses []struct {
			Status string `json:"status"`
		} `json:"statuses"`
	}
	if err = json.Unmarshal(request("GET", "/api/availability?"+adjacent.Encode(), nil, 200), &adjacentResponse); err != nil {
		t.Fatal(err)
	}
	if adjacentResponse.Statuses[0].Status != "available" {
		t.Fatalf("Adjacent booking must not block: %+v", adjacentResponse)
	}
}

func TestDateReportsPhilippineMidnightAndRefundCompletion(t *testing.T) {
	now := time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	cashier, err := s.CreateUser("Cashier", "report-cashier@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	begin := time.Date(2030, 1, 1, 16, 0, 0, 0, time.UTC) // Jan 2 midnight in Manila.
	var bookings []Booking
	var paymentIDs []int64
	for i, created := range []time.Time{begin.Add(-time.Second), begin, begin.Add(24*time.Hour - time.Second), begin.Add(24 * time.Hour)} {
		b, e := s.CreateBooking(actor, int64(i+1), now.Add(time.Hour), 1, "report-booking-"+created.Format(time.RFC3339))
		if e != nil {
			t.Fatal(e)
		}
		var itemID int64
		if e = s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", b.ID).Scan(&itemID); e != nil {
			t.Fatal(e)
		}
		receipt, e := s.RecordPayment(cashier, itemID, "cash", "", "report-payment-"+created.Format(time.RFC3339))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec("UPDATE payments SET created_at=? WHERE id=?", stamp(created), receipt.PaymentID); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec("UPDATE bookings SET created_at=? WHERE id=?", stamp(created), b.ID); e != nil {
			t.Fatal(e)
		}
		bookings = append(bookings, b)
		paymentIDs = append(paymentIDs, receipt.PaymentID)
	}
	for _, f := range []struct {
		payment            int
		created, completed time.Time
		status             string
	}{{0, begin.Add(-time.Hour), begin, "completed"}, {1, begin, begin.Add(24 * time.Hour), "completed"}, {2, begin, begin.Add(time.Hour), "pending"}} {
		if _, err = s.db.Exec("INSERT INTO refunds(payment_id,booking_id,amount_pesos,reason,status,requested_by,created_at,completed_at) VALUES(?,?,'10.25','Test',?,?,?,?)", paymentIDs[f.payment], bookings[f.payment].ID, f.status, actor.ID, stamp(f.created), stamp(f.completed)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("INSERT INTO plays(booking_id,started_at) VALUES(?,?)", bookings[0].ID, stamp(now)); err != nil {
		t.Fatal(err)
	}
	// A client cannot promote itself by supplying a cashier role.
	forged := actor
	forged.Role = "cashier"
	if _, err = s.ReportForDates(forged, "2030-01-02", "2030-01-02"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Forged role must fail: %v", err)
	}
	request("GET", "/api/reports?from=2030-01-02&to=2030-01-02", nil, 403)
	if _, err = s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	var report DateReport
	if err = json.Unmarshal(request("GET", "/api/reports?from=2030-01-02&to=2030-01-02", nil, 200), &report); err != nil {
		t.Fatal(err)
	}
	if report.Report.PaymentsPesos != 6000 || report.Report.RefundsPesos != 1025 || report.Report.NetPesos != 4975 || report.Report.BookingCount != 2 || report.Report.ActiveSessions != 1 {
		t.Fatalf("Incorrect inclusive Philippine report: %+v", report)
	}
	if len(report.Payments) != 2 || len(report.Refunds) != 1 || report.Refunds[0].PaymentID != paymentIDs[0] {
		t.Fatalf("Ledger range must use payment creation and refund completion: %+v", report)
	}
	for _, q := range []string{"from=2030-01-03&to=2030-01-02", "from=2030-02-30&to=2030-03-01", "from=2030-1-02&to=2030-01-02", "from=2030-01-02"} {
		request("GET", "/api/reports?"+q, nil, 400)
	}
}

func TestAvailabilitySuggestionRespectsBookingHorizon(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	s, _, request := newBookingLeadFixture(t, &now)
	lastStart := now.Add(30 * 24 * time.Hour)
	if _, err := s.db.Exec("INSERT INTO maintenance(pc_id,start_at,end_at,reason) VALUES(1,?,?,?)", stamp(lastStart), stamp(lastStart.Add(time.Hour)), "Repair"); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"start": {lastStart.Format(time.RFC3339)}, "slots": {"2"}}
	var result AvailabilityResult
	if err := json.Unmarshal(request("GET", "/api/availability?"+query.Encode(), nil, 200), &result); err != nil {
		t.Fatal(err)
	}
	if result.Statuses[0].Status != "maintenance" || result.Statuses[0].NextStart != nil {
		t.Fatalf("Do not suggest a start beyond the 30-day booking horizon: %+v", result.Statuses[0])
	}
}
