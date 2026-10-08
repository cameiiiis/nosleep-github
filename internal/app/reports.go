package app

// Availability details, daily financial reports, CSV exports and staff tools.

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// --- Availability explanations and financial reports ---

type AvailabilityStatus struct {
	PCID      int64      `json:"pc_id"`
	Status    string     `json:"status"`
	NextStart *time.Time `json:"next_start,omitempty"`
}

type AvailabilityResult struct {
	PCs      []PC                 `json:"pcs"`
	Statuses []AvailabilityStatus `json:"statuses"`
}

func nextQuarter(t time.Time) time.Time {
	rounded := t.Truncate(startStep)
	if rounded.Before(t) {
		rounded = rounded.Add(startStep)
	}
	return rounded.UTC()
}

// AvailabilityDetails calculates reasons and alternatives from one SQLite snapshot.
// Blocking intervals have exclusive end boundaries, matching checkout validation.
func (s *Store) AvailabilityDetails(start, end time.Time) (AvailabilityResult, error) {
	out := AvailabilityResult{PCs: []PC{}, Statuses: []AvailabilityStatus{}}
	if !validInterval(start, end) {
		return out, ErrInvalid
	}
	now := s.now()
	latest := now.Add(30 * 24 * time.Hour)
	duration := end.Sub(start)
	err := s.transact(func(tx *sql.Tx) error {
		if err := expire(tx, now); err != nil {
			return err
		}
		pcs, err := collect(tx, "SELECT id,code,tier_id,specs,enabled FROM pcs ORDER BY id", scanPC)
		if err != nil {
			return err
		}
		type blocker struct {
			pc         int64
			start, end time.Time
			kind       string
		}
		blocks, err := collect(tx, `SELECT pc_id,start_at,end_at,kind FROM (
   SELECT pc_id,start_at,end_at,'maintenance' AS kind FROM maintenance WHERE end_at>? AND start_at<?
   UNION ALL
   SELECT pc_id,start_at,start_at+1800,'reserved' AS kind FROM booking_slots WHERE start_at+1800>? AND start_at<?
  ) ORDER BY pc_id,start_at,end_at`, func(row interface{ Scan(...any) error }) (blocker, error) {
			var b blocker
			var first, last int64
			err := row.Scan(&b.pc, &first, &last, &b.kind)
			b.start = at(first)
			b.end = at(last)
			return b, err
		}, stamp(start), stamp(latest.Add(duration)), stamp(start), stamp(latest.Add(duration)))
		if err != nil {
			return err
		}
		byPC := make(map[int64][]blocker)
		for _, b := range blocks {
			byPC[b.pc] = append(byPC[b.pc], b)
		}
		for _, p := range pcs {
			status := AvailabilityStatus{PCID: p.ID, Status: "available"}
			if !p.Enabled {
				status.Status = "offline"
				out.Statuses = append(out.Statuses, status)
				continue
			}
			for _, b := range byPC[p.ID] {
				if b.start.Before(end) && b.end.After(start) {
					if b.kind == "maintenance" {
						status.Status = "maintenance"
						break
					}
					status.Status = "reserved"
				}
			}
			if status.Status == "available" {
				out.PCs = append(out.PCs, p)
			} else {
				candidate := nextQuarter(start)
				if minimum := nextQuarter(now.Add(bookingLead)); candidate.Before(minimum) {
					candidate = minimum
				}
				for _, b := range byPC[p.ID] {
					if !b.end.After(candidate) {
						continue
					}
					if !b.start.Before(candidate.Add(duration)) {
						break
					}
					candidate = nextQuarter(b.end)
				}
				if !candidate.After(latest) {
					status.NextStart = &candidate
				}
			}
			out.Statuses = append(out.Statuses, status)
		}
		return nil
	})
	return out, err
}

type DateReport struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Report   Report    `json:"report"`
	Payments []Payment `json:"payments"`
	Refunds  []Refund  `json:"refunds"`
}

// ReportForDates counts collections and money returned on their transaction dates.
// Dates are inclusive calendar days in the Philippines, independent of host timezone.
func (s *Store) ReportForDates(actor User, from, to string) (DateReport, error) {
	out := DateReport{From: from, To: to, Payments: []Payment{}, Refunds: []Refund{}}
	zone := time.FixedZone("Philippine time", 8*60*60)
	first, err1 := time.ParseInLocation("2006-01-02", from, zone)
	last, err2 := time.ParseInLocation("2006-01-02", to, zone)
	if err1 != nil || err2 != nil || first.Format("2006-01-02") != from || last.Format("2006-01-02") != to || last.Before(first) {
		return out, inputError("Choose valid dates with the end date on or after the start date.")
	}
	snap, err := s.Snapshot(actor)
	if err != nil {
		return out, err
	}
	if snap.User.Role != "cashier" && snap.User.Role != "admin" {
		return out, ErrForbidden
	}
	exclusiveEnd := last.AddDate(0, 0, 1)
	inRange := func(t time.Time) bool { return !t.Before(first) && t.Before(exclusiveEnd) }
	for _, p := range snap.Payments {
		if inRange(p.CreatedAt) {
			out.Payments = append(out.Payments, p)
			out.Report.PaymentsPesos += p.AmountPesos
		}
	}
	for _, f := range snap.Refunds {
		if f.Status == "completed" && f.CompletedAt != nil && inRange(*f.CompletedAt) {
			out.Refunds = append(out.Refunds, f)
			out.Report.RefundsPesos += f.AmountPesos
		}
	}
	for _, b := range snap.Bookings {
		if inRange(b.CreatedAt) {
			out.Report.BookingCount++
		}
	}
	out.Report.NetPesos = out.Report.PaymentsPesos - out.Report.RefundsPesos
	out.Report.ActiveSessions = snap.Report.ActiveSessions
	return out, nil
}

func (a *api) registerOperationsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reports", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		result, err := a.store.ReportForDates(u, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, http.StatusOK, result)
	})
}

// --- Reference checks, CSV export and manual backup download ---

type PaymentReferenceMatch struct {
	ID        int64  `json:"id"`
	BookingID int64  `json:"booking_id"`
	Reference string `json:"reference"`
}
type PaymentReferenceResult struct {
	Count    int                     `json:"count"`
	Payments []PaymentReferenceMatch `json:"payments"`
}

// Matching references are an advisory check; repeated references remain legal.
func (s *Store) PaymentReferences(actor User, reference string) (PaymentReferenceResult, error) {
	out := PaymentReferenceResult{Payments: []PaymentReferenceMatch{}}
	reference = strings.TrimSpace(reference)
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		if reference == "" {
			return nil
		}
		matches, err := collect(tx, "SELECT id,booking_id,reference FROM payments WHERE method='external' ORDER BY id", func(row interface{ Scan(...any) error }) (PaymentReferenceMatch, error) {
			var p PaymentReferenceMatch
			err := row.Scan(&p.ID, &p.BookingID, &p.Reference)
			return p, err
		})
		if err != nil {
			return err
		}
		for _, p := range matches {
			if strings.EqualFold(strings.TrimSpace(p.Reference), reference) {
				out.Payments = append(out.Payments, p)
			}
		}
		out.Count = len(out.Payments)
		return nil
	})
	return out, err
}

// The authorizing transaction is closed before VACUUM uses our single connection.
func (s *Store) DatabaseBackup(actor User) ([]byte, error) {
	if err := s.transact(func(tx *sql.Tx) error { return require(tx, actor, "admin") }); err != nil {
		return nil, err
	}
	path, cleanup, err := sqliteSnapshot(s.db, "")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return os.ReadFile(path)
}

type reportBooking struct {
	ID                  int64
	Reference, Customer string
}

func (s *Store) reportCSV(actor User, from, to string) ([]byte, error) {
	report, err := s.ReportForDates(actor, from, to)
	if err != nil {
		return nil, err
	}
	details := map[int64]reportBooking{}
	err = s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		rows, err := collect(tx, `SELECT b.id,b.reference,COALESCE(w.guest_name,u.name) FROM bookings b JOIN users u ON u.id=b.user_id LEFT JOIN walk_ins w ON w.booking_id=b.id`, func(row interface{ Scan(...any) error }) (reportBooking, error) {
			var b reportBooking
			err := row.Scan(&b.ID, &b.Reference, &b.Customer)
			return b, err
		})
		if err != nil {
			return err
		}
		for _, b := range rows {
			details[b.ID] = b
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var data bytes.Buffer
	writer := csv.NewWriter(&data)
	if err = writer.Write([]string{"from", "to", "type", "id", "booking_id", "booking_reference", "customer", "method", "reference", "reason", "recorded_at", "amount_pesos"}); err != nil {
		return nil, err
	}
	zone := time.FixedZone("Asia/Manila", 8*60*60)
	write := func(kind string, id, bookingID int64, method, reference, reason string, recorded time.Time, amount Pesos) error {
		b := details[bookingID]
		return writer.Write([]string{from, to, kind, strconv.FormatInt(id, 10), strconv.FormatInt(bookingID, 10), csvText(b.Reference), csvText(b.Customer), csvText(method), csvText(reference), csvText(reason), recorded.In(zone).Format(time.RFC3339), amount.String()})
	}
	for _, p := range report.Payments {
		if err = write("payment", p.ID, p.BookingID, p.Method, p.Reference, "", p.CreatedAt, p.AmountPesos); err != nil {
			return nil, err
		}
	}
	for _, r := range report.Refunds {
		if err = write("refund", r.ID, r.BookingID, "", "", r.Reason, *r.CompletedAt, r.AmountPesos); err != nil {
			return nil, err
		}
	}
	for _, total := range []struct {
		name   string
		amount Pesos
	}{{"payments_total", report.Report.PaymentsPesos}, {"refunds_total", report.Report.RefundsPesos}, {"net_total", report.Report.NetPesos}} {
		if err = writer.Write([]string{from, to, total.name, "", "", "", "", "", "", "", "", total.amount.String()}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return data.Bytes(), writer.Error()
}

func csvText(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") || (len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
		return "'" + value
	}
	return value
}

func (a *api) registerToolsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/payment-reference", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		result, err := a.store.PaymentReferences(u, r.URL.Query().Get("reference"))
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/reports.csv", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		data, err := a.store.reportCSV(u, from, to)
		if err != nil {
			a.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nosleep-report-%s-to-%s.csv"`, from, to))
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /api/admin/backup", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		data, err := a.store.DatabaseBackup(u)
		if err != nil {
			a.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nosleep-backup-%s.sqlite"`, a.store.now().Format("20060102-150405")))
		_, _ = w.Write(data)
	})
}
