package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func cashReferenceFixture(t *testing.T, path string) (*Store, User, User) {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.Now = func() time.Time { return time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC) }
	if err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	player, err := s.CreateUser("Player", "cash-player@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	cashier, err := s.CreateUser("Cashier", "cash-staff@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE users SET role='cashier' WHERE id=?", cashier.ID); err != nil {
		t.Fatal(err)
	}
	return s, player, cashier
}

func cashReferenceItem(t *testing.T, s *Store, player User, pcID int64) int64 {
	t.Helper()
	b, err := s.CreateBooking(player, pcID, s.now().Add(time.Hour), 1, fmt.Sprintf("cash-booking-%d", pcID))
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err := s.db.QueryRow("SELECT id FROM booking_items WHERE booking_id=?", b.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	return itemID
}

func paymentReference(t *testing.T, s *Store, receipt Receipt) string {
	t.Helper()
	var reference string
	if err := s.db.QueryRow("SELECT p.reference FROM payments p JOIN receipts r ON r.payment_id=p.id WHERE r.id=? AND p.id=?", receipt.ID, receipt.PaymentID).Scan(&reference); err != nil {
		t.Fatal(err)
	}
	return reference
}

func TestCashReferencesSequenceAndRetries(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	firstItem := cashReferenceItem(t, s, player, 1)
	first, err := s.RecordPayment(cashier, firstItem, "cash", "", "cash-first")
	if err != nil {
		t.Fatal(err)
	}
	if got := paymentReference(t, s, first); got != "CASH-000001" {
		t.Fatalf("First cash payment reference = %q", got)
	}
	retry, err := s.RecordPayment(cashier, firstItem, "cash", "ignored-client-reference", "cash-first")
	if err != nil || retry.ID != first.ID || retry.Number != first.Number {
		t.Fatalf("Cash retry must return the existing receipt: %+v, %v", retry, err)
	}
	if got := paymentReference(t, s, retry); got != "CASH-000001" {
		t.Fatalf("Cash retry changed the reference to %q", got)
	}
	externalItem := cashReferenceItem(t, s, player, 2)
	if _, err := s.RecordPayment(cashier, externalItem, "external", "  ", "blank-external"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("External payment still requires a reference: %v", err)
	}
	external, err := s.RecordPayment(cashier, externalItem, "external", "  TRANSFER-42  ", "external-payment")
	if err != nil || paymentReference(t, s, external) != "TRANSFER-42" {
		t.Fatalf("External reference must be preserved and trimmed: %+v, %v", external, err)
	}
	if _, err := s.RecordPayment(cashier, externalItem, "external", "DIFFERENT", "external-payment"); !errors.Is(err, ErrConflict) {
		t.Fatalf("External retry cannot change its reference: %v", err)
	}
	if _, err := s.RecordPayment(cashier, 99999, "cash", "", "invalid-item"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Invalid cash payment must not be recorded: %v", err)
	}
	second, err := s.RecordPayment(cashier, cashReferenceItem(t, s, player, 3), "cash", "client-number-is-ignored", "cash-second")
	if err != nil || paymentReference(t, s, second) != "CASH-000002" {
		t.Fatalf("Only recorded cash payments advance the sequence: %+v, %v", second, err)
	}
}

func TestCashReferencesContinueAfterRestartAndLegacyNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cash.db")
	s, player, cashier := cashReferenceFixture(t, path)
	legacy, err := s.RecordPayment(cashier, cashReferenceItem(t, s, player, 1), "cash", "LEGACY-TILL", "legacy-payment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE payments SET reference='CASH-000009' WHERE id=?", legacy.PaymentID); err != nil {
		t.Fatal(err)
	}
	itemID := cashReferenceItem(t, s, player, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reopened.Now = s.Now
	next, err := reopened.RecordPayment(cashier, itemID, "cash", "", "after-restart")
	if err != nil || paymentReference(t, reopened, next) != "CASH-000010" {
		t.Fatalf("Restart must continue past existing generated references: %+v, %v", next, err)
	}
	if paymentReference(t, reopened, legacy) != "CASH-000009" {
		t.Fatal("Recording new cash must not rewrite an existing reference")
	}
}

func TestCashReferencesConcurrentPaymentsAreUnique(t *testing.T) {
	s, player, cashier := cashReferenceFixture(t, ":memory:")
	itemIDs := []int64{cashReferenceItem(t, s, player, 1), cashReferenceItem(t, s, player, 2)}
	type result struct {
		receipt Receipt
		err     error
	}
	results := make(chan result, len(itemIDs))
	for _, itemID := range itemIDs {
		go func(itemID int64) {
			receipt, err := s.RecordPayment(cashier, itemID, "cash", "", fmt.Sprintf("concurrent-%d", itemID))
			results <- result{receipt, err}
		}(itemID)
	}
	var references []string
	for range itemIDs {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		references = append(references, paymentReference(t, s, result.receipt))
	}
	sort.Strings(references)
	if references[0] != "CASH-000001" || references[1] != "CASH-000002" {
		t.Fatalf("Concurrent cash payments must get distinct consecutive references: %v", references)
	}
}
