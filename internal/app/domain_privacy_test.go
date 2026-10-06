package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemotedCashierCannotAccessWalkInAsCustomer(t *testing.T) {
	s, _, cashier := cashReferenceFixture(t, ":memory:")
	start := s.now().Add(time.Hour)
	guest, err := s.CreateWalkIn(cashier, 1, start, 2, "private-guest", "Private Guest")
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.CreateBooking(cashier, 2, start, 1, "own-online")
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err = s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", guest.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecordPayment(cashier, itemID, "cash", "", "guest-pay")
	if err != nil {
		t.Fatal(err)
	}
	extension, err := s.ExtendBooking(cashier, guest.ID, 1, "guest-extension")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestRefund(cashier, receipt.PaymentID, "Private refund"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET role='customer' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	t.Run("snapshot", func(t *testing.T) {
		snap, err := s.Snapshot(cashier)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Bookings) != 1 || snap.Bookings[0].ID != own.ID || len(snap.WalkIns) != 0 || len(snap.Payments) != 0 || len(snap.Receipts) != 0 || len(snap.Refunds) != 0 {
			t.Fatalf("Guest data leaked to demoted cashier: %+v", snap)
		}
		for _, event := range snap.Audit {
			if event.Action == "payment.recorded" || event.Action == "refund.requested" || event.Action == "extension.held" || event.Action == "booking.created" && event.EntityID == guest.ID {
				t.Fatalf("Guest audit leaked: %+v", event)
			}
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		if err := s.CancelBooking(cashier, guest.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest cancellation must be forbidden: %v", err)
		}
	})
	t.Run("extension retry", func(t *testing.T) {
		if _, err := s.ExtendBooking(cashier, guest.ID, 1, "guest-extension"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest extension retry must be forbidden: %v (item %d)", err, extension.ID)
		}
	})
	t.Run("new extension", func(t *testing.T) {
		if _, err := s.ExtendBooking(cashier, guest.ID, 1, "new-guest-extension"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest extension must be forbidden: %v", err)
		}
	})
	t.Run("refund", func(t *testing.T) {
		if _, err := s.RequestRefund(cashier, receipt.PaymentID, "Again"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Guest refund must be forbidden: %v", err)
		}
	})
	if err = s.CancelBooking(cashier, own.ID); err != nil {
		t.Fatalf("Demotion must preserve ordinary own booking rights: %v", err)
	}
}

func TestStaffSnapshotHasOnlyBookedCustomerNames(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	if _, err := s.CreateBooking(player, 1, s.now().Add(time.Hour), 1, "customer-name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWalkIn(cashier, 2, s.now().Add(time.Hour), 1, "staff-guest", "Guest Name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("Unbooked", "unused@example.com", "hash"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []User{cashier, player} {
		snap, err := s.Snapshot(actor)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		var names []map[string]any
		if err = json.Unmarshal(data["customers"], &names); err != nil {
			t.Fatalf("Snapshot needs customers array: %v", err)
		}
		if actor.ID == player.ID {
			if len(names) != 0 {
				t.Fatalf("Customer received other names: %+v", names)
			}
			continue
		}
		if len(names) != 1 || len(names[0]) != 2 || names[0]["name"] != player.Name || names[0]["id"] != float64(player.ID) {
			t.Fatalf("Staff needs only booked customer id/name: %+v", names)
		}
		if len(snap.Users) != 0 {
			t.Fatal("Cashier must not get admin user directory")
		}
	}
}

type maintenanceDetailSetter interface {
	SetMaintenanceDetails(User, int64, time.Time, time.Time, string, string) error
}

func TestMaintenancePrivateNotesAndPublicReason(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	if _, err := s.db.Exec("UPDATE users SET role='admin' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	start, end := s.now(), s.now().Add(time.Hour)
	if err := s.SetMaintenance(cashier, 1, start, end, "Legacy secret staff note"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(player)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap.Maintenance[0].Reason, "secret") {
		t.Fatal("Legacy maintenance reason leaked to customer")
	}
	setter, ok := any(s).(maintenanceDetailSetter)
	if !ok {
		t.Fatal("Store needs separate private and public maintenance reasons")
	}
	if err = setter.SetMaintenanceDetails(cashier, 2, start, end, "Secret hardware serial", "  Station repair  "); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(player)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snap)
	if snap.Maintenance[1].Reason != "Station repair" || strings.Contains(string(raw), "Secret hardware serial") || strings.Contains(string(raw), "Legacy secret") {
		t.Fatalf("Customer maintenance was not sanitized: %s", raw)
	}
	staff, err := s.Snapshot(cashier)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(staff)
	if !strings.Contains(string(raw), "Secret hardware serial") || !strings.Contains(string(raw), `"public_reason":"Station repair"`) {
		t.Fatalf("Staff lost maintenance details: %s", raw)
	}
}

func TestMaintenanceLegacyMigrationRetainsPrivateNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-maintenance.db")
	s, player, cashier := cashReferenceFixture(t, path)
	if _, err := s.db.Exec("DROP TABLE maintenance; CREATE TABLE maintenance(id INTEGER PRIMARY KEY,pc_id INTEGER NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,reason TEXT NOT NULL); INSERT INTO maintenance VALUES(9,1,1893499200,1893502800,'Legacy confidential')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reopened, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		var private, public string
		err = reopened.db.QueryRow("SELECT reason,public_reason FROM maintenance WHERE id=9").Scan(&private, &public)
		if err != nil {
			reopened.Close()
			t.Fatal(err)
		}
		if private != "Legacy confidential" || public != "" {
			reopened.Close()
			t.Fatalf("Unsafe migration: private=%q public=%q", private, public)
		}
		customer, err := reopened.Snapshot(player)
		if err != nil {
			t.Fatal(err)
		}
		staff, err := reopened.Snapshot(cashier)
		if err != nil {
			t.Fatal(err)
		}
		if customer.Maintenance[0].Reason != "" || staff.Maintenance[0].Reason != private {
			t.Fatal("Legacy note privacy lost")
		}
		if err = reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
