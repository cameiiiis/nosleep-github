package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingDatabaseConvertsToPesosOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile("testdata/legacy.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(legacy)); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO users VALUES(1,'Admin','admin@example.com','hash','admin',1,1),(2,'Player','player@example.com','hash','customer',1,1),(3,'Cashier','cashier@example.com','hash','cashier',1,1);
INSERT INTO auth_sessions VALUES('existing-session',2,9999999999);
INSERT INTO tiers VALUES(1,'Standard',3055);
INSERT INTO pcs VALUES(1,'PC-01',1,'Test PC',1);
INSERT INTO bookings VALUES(1,'NS-000001',2,1,2000000000,2000007200,'confirmed',2000000000,'original-booking',1);
INSERT INTO booking_items VALUES(1,1,'base',4,3055,12220,'paid',2000000000,2000000000,2000007200,NULL);
INSERT INTO booking_slots VALUES(1,2000000000,1);
INSERT INTO payments VALUES(1,1,1,12220,'cash','Original payment',3,'original-payment',1);
INSERT INTO receipts VALUES(1,'RC-000001',1,1,'Original receipt',12220,1);
INSERT INTO refunds VALUES(1,1,1,12220,'Test refund','completed',2,1,3,1,2);
INSERT INTO audit VALUES(1,3,'payment.recorded',1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range []struct{ query, want string }{
			{"SELECT price_pesos FROM tiers WHERE id=1", "30.55"},
			{"SELECT unit_pesos FROM booking_items WHERE id=1", "30.55"},
			{"SELECT amount_pesos FROM booking_items WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM payments WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM receipts WHERE id=1", "122.20"},
			{"SELECT amount_pesos FROM refunds WHERE id=1", "122.20"},
			{"SELECT reference FROM payments WHERE id=1", "Original payment"},
			{"SELECT description FROM receipts WHERE id=1", "Original receipt"},
			{"SELECT token_hash FROM auth_sessions", "existing-session"},
		} {
			var got string
			if err := s.db.QueryRow(check.query).Scan(&got); err != nil {
				s.Close()
				t.Fatal(err)
			}
			if got != check.want {
				s.Close()
				t.Fatalf("pass %d: %s returned %q, want %q", pass, check.query, got, check.want)
			}
		}
		var enabled int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
			s.Close()
			t.Fatalf("Foreign keys disabled: %d, %v", enabled, err)
		}
		rows, err := s.db.Query("PRAGMA foreign_key_check")
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		broken := rows.Next()
		rowErr := rows.Err()
		rows.Close()
		if broken || rowErr != nil {
			s.Close()
			t.Fatalf("Broken references after migration: %v", rowErr)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
