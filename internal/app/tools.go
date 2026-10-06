package app

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
