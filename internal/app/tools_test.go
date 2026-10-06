package app

import (
	"bytes"
	"encoding/csv"
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaffCSVAndReferenceLookupAreSafeAndUseInclusiveManilaDates(t *testing.T) {
	now := time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC)
	s, actor, request := newBookingLeadFixture(t, &now)
	request("GET", "/api/reports.csv?from=2030-01-02&to=2030-01-02", nil, 403)
	request("GET", "/api/payment-reference?reference=bank", nil, 403)
	if _, err := s.db.Exec("UPDATE users SET role='cashier',name='=unsafe' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	var payments []int64
	for i, created := range []time.Time{time.Date(2030, 1, 1, 15, 59, 59, 0, time.UTC), time.Date(2030, 1, 1, 16, 0, 0, 0, time.UTC), time.Date(2030, 1, 2, 15, 59, 59, 0, time.UTC), time.Date(2030, 1, 2, 16, 0, 0, 0, time.UTC)} {
		b, err := s.CreateBooking(actor, int64(i+1), now.Add(time.Hour), 1, string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		var item int64
		_ = s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", b.ID).Scan(&item)
		receipt, err := s.RecordPayment(actor, item, "external", "=BANK", "paid-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("UPDATE payments SET created_at=? WHERE id=?", stamp(created), receipt.PaymentID); err != nil {
			t.Fatal(err)
		}
		payments = append(payments, receipt.PaymentID)
	}
	var duplicate struct {
		Count    int `json:"count"`
		Payments []struct {
			ID int64 `json:"id"`
		} `json:"payments"`
	}
	if err := json.Unmarshal(request("GET", "/api/payment-reference?reference=%20%3DbAnK%20", nil, 200), &duplicate); err != nil || duplicate.Count != 4 || len(duplicate.Payments) != 4 {
		t.Fatalf("Lookup must be advisory and case-insensitive: %+v %v", duplicate, err)
	}
	raw := request("GET", "/api/reports.csv?from=2030-01-02&to=2030-01-02", nil, 200)
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var collections int
	for _, row := range rows {
		for _, field := range row {
			if field == "payment" {
				collections++
			}
			if field == "=unsafe" || field == "=BANK" {
				t.Fatal("CSV formula was not neutralized")
			}
		}
	}
	if collections != 2 || !bytes.Contains(raw, []byte("30.00")) || !bytes.Contains(raw, []byte("'=BANK")) {
		t.Fatalf("CSV must use inclusive Manila dates and decimal pesos: %s", raw)
	}
	request("GET", "/api/reports.csv?from=bad&to=bad", nil, 400)
	if _, err = s.db.Exec("UPDATE users SET active=0 WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/payment-reference?reference=bank", nil, 401)
}

func TestAdminBackupIncludesLiveWALAndLeavesSourceUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	s, player, cashier := cashReferenceFixture(t, path)
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecordPayment(cashier, cashReferenceItem(t, s, player, 1), "external", "LIVE-WAL", "backup-payment")
	if err != nil {
		t.Fatal(err)
	}
	cashier.Role = "admin"
	if _, err = s.DatabaseBackup(cashier); err != ErrForbidden {
		t.Fatalf("Forged admin role must fail: %v", err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='admin' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	walBefore, _ := os.ReadFile(path + "-wal")
	if len(walBefore) == 0 {
		t.Fatal("Need WAL records")
	}
	data, err := s.DatabaseBackup(cashier)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	walAfter, _ := os.ReadFile(path + "-wal")
	if !bytes.Equal(before, after) || !bytes.Equal(walBefore, walAfter) {
		t.Fatal("Backup changed source database")
	}
	if !strings.HasPrefix(string(data), "SQLite format 3") {
		t.Fatal("Backup is not SQLite")
	}
	downloaded := filepath.Join(t.TempDir(), "download.db")
	if err = os.WriteFile(downloaded, data, 0600); err != nil {
		t.Fatal(err)
	}
	copyStore, err := OpenStore(downloaded)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	var reference string
	if err = copyStore.db.QueryRow("SELECT reference FROM payments WHERE id=?", receipt.PaymentID).Scan(&reference); err != nil || reference != "LIVE-WAL" {
		t.Fatalf("WAL payment missing: %q %v", reference, err)
	}
	if _, err = s.db.Exec("UPDATE users SET active=0 WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DatabaseBackup(cashier); err != ErrForbidden {
		t.Fatalf("Disabled admin must fail: %v", err)
	}
}
