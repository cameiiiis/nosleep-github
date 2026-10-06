package app

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const slot = 30 * time.Minute
const bookingLead = 15 * time.Minute
const startStep = 15 * time.Minute

func validSlot(start time.Time, slots int, now time.Time) bool {
	return slots >= 1 && slots <= 16 && !start.Before(now.Add(bookingLead)) && !start.After(now.Add(30*24*time.Hour)) && start.Nanosecond() == 0 && start.UTC().Unix()%int64(startStep.Seconds()) == 0
}
func occupied(tx *sql.Tx, pcID int64, start, end time.Time) (bool, error) {
	var n int
	e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM maintenance WHERE pc_id=? AND start_at<? AND end_at>?) OR EXISTS(SELECT 1 FROM booking_slots WHERE pc_id=? AND start_at+1800>? AND start_at<?)", pcID, stamp(end), stamp(start), pcID, stamp(start), stamp(end)).Scan(&n)
	return n != 0, e
}
func reserve(tx *sql.Tx, pcID, itemID int64, start time.Time, slots int) error {
	for k := 0; k < slots; k++ {
		if _, e := tx.Exec("INSERT INTO booking_slots(pc_id,start_at,item_id) VALUES(?,?,?)", pcID, stamp(start.Add(time.Duration(k)*slot)), itemID); e != nil {
			return ErrConflict
		}
	}
	return nil
}
func (s *Store) CreateBooking(actor User, pcID int64, start time.Time, slots int, key string) (Booking, error) {
	return s.createBooking(actor, pcID, start, slots, key, "")
}

func (s *Store) createBooking(actor User, pcID int64, start time.Time, slots int, key, guestName string) (Booking, error) {
	var out Booking
	now := s.now()
	start = start.UTC()
	key = strings.TrimSpace(key)
	if key == "" || slots < 1 || slots > 16 {
		return out, ErrInvalid
	}
	e := s.transact(func(tx *sql.Tx) error {
		roles := []string{"customer", "cashier", "admin"}
		if guestName != "" {
			roles = []string{"cashier", "admin"}
		}
		if e := require(tx, actor, roles...); e != nil {
			return e
		}
		if e := expire(tx, now); e != nil {
			return e
		}
		existing, e := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE checkout_key=?", key))
		if e == nil {
			var originalGuest string
			guestErr := tx.QueryRow("SELECT guest_name FROM walk_ins WHERE booking_id=?", existing.ID).Scan(&originalGuest)
			if guestErr != nil && !errors.Is(guestErr, sql.ErrNoRows) {
				return guestErr
			}
			if originalGuest != guestName {
				return ErrConflict
			}
			var originalSlots int
			if e = tx.QueryRow("SELECT slots FROM booking_items WHERE booking_id=? AND kind='base'", existing.ID).Scan(&originalSlots); e != nil {
				return e
			}
			if existing.UserID != actor.ID || existing.PCID != pcID || !existing.StartAt.Equal(start) || originalSlots != slots {
				return ErrConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if !validSlot(start, slots, now) {
			return ErrInvalid
		}
		out, e = createReservation(tx, actor, pcID, start, slots, key, guestName, now)
		return e
	})
	return out, e
}
func (s *Store) RecordPayment(actor User, itemID int64, method, reference, key string) (Receipt, error) {
	var out Receipt
	method = strings.TrimSpace(method)
	reference = strings.TrimSpace(reference)
	key = strings.TrimSpace(key)
	if (method != "cash" && method != "external") || (method == "external" && reference == "") || key == "" {
		return out, ErrInvalid
	}
	now := s.now()
	e := s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "cashier", "admin"); e != nil {
			return e
		}
		if e := expire(tx, now); e != nil {
			return e
		}
		r, e := scanReceipt(tx.QueryRow("SELECT r."+"id,r.number,r.payment_id,r.booking_id,r.description,r.amount_pesos,r.created_at FROM receipts r JOIN payments p ON p.id=r.payment_id WHERE p.payment_key=?", key))
		if e == nil {
			var paidItem int64
			var oldMethod, oldRef string
			if e := tx.QueryRow("SELECT item_id,method,reference FROM payments WHERE id=?", r.PaymentID).Scan(&paidItem, &oldMethod, &oldRef); e != nil {
				return e
			}
			if paidItem != itemID || oldMethod != method || (method == "external" && oldRef != reference) {
				return ErrConflict
			}
			out = r
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		i, e := scanItem(tx.QueryRow("SELECT "+itemCols+" FROM booking_items WHERE id=?", itemID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if i.Status != "held" || !now.Before(i.HoldUntil) {
			return ErrConflict
		}
		b, e := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", i.BookingID))
		if e != nil {
			return e
		}
		if i.Kind == "base" {
			if b.Status != "held" || !now.Before(b.StartAt) {
				return ErrConflict
			}
		} else {
			if b.Status != "confirmed" || !b.EndAt.Equal(i.ExpectedEnd) || !now.Before(b.EndAt) {
				return ErrConflict
			}
			var active int
			e = tx.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE item_id=?", i.ID).Scan(&active)
			if e != nil {
				return e
			}
			if active != i.Slots {
				return ErrConflict
			}
		}
		out, e = recordPayment(tx, actor, b, i, method, reference, key, now)
		return e
	})
	return out, e
}
func (s *Store) CancelBooking(actor User, bookingID int64) error {
	now := s.now()
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "customer", "cashier", "admin"); e != nil {
			return e
		}
		if e := expire(tx, now); e != nil {
			return e
		}
		b, e := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", bookingID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if e = bookingAccess(tx, actor, b.ID); e != nil {
			return e
		}
		if b.Status != "held" && b.Status != "confirmed" {
			return ErrConflict
		}
		if !now.Before(b.StartAt) {
			return ErrConflict
		}
		var played int
		if e = tx.QueryRow("SELECT COUNT(*) FROM plays WHERE booking_id=?", bookingID).Scan(&played); e != nil {
			return e
		}
		if played > 0 {
			return ErrConflict
		}
		if _, e = tx.Exec("DELETE FROM booking_slots WHERE item_id IN (SELECT id FROM booking_items WHERE booking_id=?)", bookingID); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE booking_items SET status='cancelled' WHERE booking_id=? AND status='held'", bookingID); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE bookings SET status='cancelled' WHERE id=?", bookingID); e != nil {
			return e
		}
		rows, e := tx.Query("SELECT id,amount_pesos FROM payments WHERE booking_id=?", bookingID)
		if e != nil {
			return e
		}
		type paid struct {
			id     int64
			amount Pesos
		}
		var payments []paid
		for rows.Next() {
			var p paid
			if e = rows.Scan(&p.id, &p.amount); e != nil {
				rows.Close()
				return e
			}
			payments = append(payments, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, p := range payments {
			reserved, e := reservedRefundAmount(tx, p.id)
			if e != nil {
				return e
			}
			if reserved >= p.amount {
				continue
			}
			if _, e = tx.Exec("INSERT INTO refunds(payment_id,booking_id,amount_pesos,reason,status,requested_by,created_at) VALUES(?,?,?,'Booking cancelled','pending',?,?)", p.id, bookingID, p.amount-reserved, actor.ID, stamp(now)); e != nil {
				return e
			}
		}
		return audit(tx, actor.ID, "booking.cancelled", bookingID, now)
	})
}
func (s *Store) StartPlay(actor User, bookingID int64) error {
	now := s.now()
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "cashier", "admin"); e != nil {
			return e
		}
		if e := expire(tx, now); e != nil {
			return e
		}
		b, e := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", bookingID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if b.Status != "confirmed" || now.Before(b.StartAt) || !now.Before(b.EndAt) {
			return ErrConflict
		}
		var stationBusy bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM plays p JOIN bookings b ON b.id=p.booking_id WHERE b.pc_id=? AND p.ended_at IS NULL)", b.PCID).Scan(&stationBusy); e != nil {
			return e
		}
		if stationBusy {
			return ErrConflict
		}
		if _, e = tx.Exec("INSERT INTO plays(booking_id,started_at) VALUES(?,?)", bookingID, stamp(now)); e != nil {
			return ErrConflict
		}
		return audit(tx, actor.ID, "play.started", bookingID, now)
	})
}
func (s *Store) EndPlay(actor User, bookingID int64) error {
	now := s.now()
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "cashier", "admin"); e != nil {
			return e
		}
		r, e := tx.Exec("UPDATE plays SET ended_at=? WHERE booking_id=? AND ended_at IS NULL", stamp(now), bookingID)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return ErrConflict
		}
		if _, e = tx.Exec("UPDATE bookings SET status='completed' WHERE id=?", bookingID); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE booking_items SET status='cancelled' WHERE booking_id=? AND status='held'", bookingID); e != nil {
			return e
		}
		if _, e = tx.Exec("DELETE FROM booking_slots WHERE item_id IN (SELECT id FROM booking_items WHERE booking_id=?)", bookingID); e != nil {
			return e
		}
		return audit(tx, actor.ID, "play.ended", bookingID, now)
	})
}
func (s *Store) ExtendBooking(actor User, bookingID int64, slots int, key string) (BookingItem, error) {
	var out BookingItem
	now := s.now()
	key = strings.TrimSpace(key)
	if slots < 1 || slots > 16 || key == "" {
		return out, ErrInvalid
	}
	e := s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "customer", "cashier", "admin"); e != nil {
			return e
		}
		if e := expire(tx, now); e != nil {
			return e
		}
		old, e := scanItem(tx.QueryRow("SELECT "+itemCols+" FROM booking_items WHERE extension_key=?", key))
		if e == nil {
			if e = bookingAccess(tx, actor, old.BookingID); e != nil {
				return e
			}
			if old.BookingID != bookingID || old.Slots != slots {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		b, e := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", bookingID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if e = bookingAccess(tx, actor, b.ID); e != nil {
			return e
		}
		if b.Status != "confirmed" || !now.Before(b.EndAt) {
			return ErrConflict
		}
		var ended int
		e = tx.QueryRow("SELECT COUNT(*) FROM plays WHERE booking_id=? AND ended_at IS NOT NULL", b.ID).Scan(&ended)
		if e != nil {
			return e
		}
		if ended > 0 {
			return ErrConflict
		}
		var pending int
		e = tx.QueryRow("SELECT COUNT(*) FROM booking_items WHERE booking_id=? AND kind='extension' AND status='held'", b.ID).Scan(&pending)
		if e != nil {
			return e
		}
		if pending > 0 {
			return ErrConflict
		}
		proposed := b.EndAt.Add(time.Duration(slots) * slot)
		busy, e := occupied(tx, b.PCID, b.EndAt, proposed)
		if e != nil {
			return e
		}
		if busy {
			return ErrConflict
		}
		var price Pesos
		e = tx.QueryRow("SELECT t.price_pesos FROM pcs p JOIN tiers t ON t.id=p.tier_id WHERE p.id=?", b.PCID).Scan(&price)
		if e != nil {
			return e
		}
		hold := now.Add(10 * time.Minute)
		amount := sessionPrice(price, b.EndAt, slots)
		result, e := tx.Exec("INSERT INTO booking_items(booking_id,kind,slots,unit_pesos,amount_pesos,status,hold_until,expected_end,proposed_end,extension_key) VALUES(?,'extension',?,?,?,'held',?,?,?,?)", bookingID, slots, price, amount, stamp(hold), stamp(b.EndAt), stamp(proposed), key)
		if e != nil {
			return e
		}
		id, _ := result.LastInsertId()
		if e = reserve(tx, b.PCID, id, b.EndAt, slots); e != nil {
			return e
		}
		if e = audit(tx, actor.ID, "extension.held", id, now); e != nil {
			return e
		}
		out = BookingItem{id, bookingID, "extension", slots, price, amount, "held", hold, b.EndAt, proposed}
		return nil
	})
	return out, e
}

func createReservation(tx *sql.Tx, actor User, pcID int64, start time.Time, slots int, key, guestName string, now time.Time) (Booking, error) {
	end := start.Add(time.Duration(slots) * slot)
	var enabled bool
	var price Pesos
	e := tx.QueryRow("SELECT p.enabled,t.price_pesos FROM pcs p JOIN tiers t ON t.id=p.tier_id WHERE p.id=?", pcID).Scan(&enabled, &price)
	if errors.Is(e, sql.ErrNoRows) {
		return Booking{}, ErrNotFound
	}
	if e != nil {
		return Booking{}, e
	}
	if !enabled {
		return Booking{}, ErrConflict
	}
	busy, e := occupied(tx, pcID, start, end)
	if e != nil {
		return Booking{}, e
	}
	if busy {
		return Booking{}, ErrConflict
	}
	hold := now.Add(10 * time.Minute)
	r, e := tx.Exec("INSERT INTO bookings(reference,user_id,pc_id,start_at,end_at,status,hold_until,checkout_key,created_at) VALUES('',?,?,?,?,?,?,?,?)", actor.ID, pcID, stamp(start), stamp(end), "held", stamp(hold), key, stamp(now))
	if e != nil {
		return Booking{}, e
	}
	id, _ := r.LastInsertId()
	ref := fmt.Sprintf("NS-%06d", id)
	if _, e = tx.Exec("UPDATE bookings SET reference=? WHERE id=?", ref, id); e != nil {
		return Booking{}, e
	}
	r, e = tx.Exec("INSERT INTO booking_items(booking_id,kind,slots,unit_pesos,amount_pesos,status,hold_until,expected_end,proposed_end) VALUES(?,'base',?,?,?,'held',?,?,?)", id, slots, price, sessionPrice(price, start, slots), stamp(hold), stamp(start), stamp(end))
	if e != nil {
		return Booking{}, e
	}
	itemID, _ := r.LastInsertId()
	if e = reserve(tx, pcID, itemID, start, slots); e != nil {
		return Booking{}, e
	}
	if guestName != "" {
		if _, e = tx.Exec("INSERT INTO walk_ins(booking_id,guest_name) VALUES(?,?)", id, guestName); e != nil {
			return Booking{}, e
		}
	}
	if e = audit(tx, actor.ID, "booking.created", id, now); e != nil {
		return Booking{}, e
	}
	out := Booking{id, ref, actor.ID, pcID, start, end, "held", hold, now}
	return out, nil
}

func recordPayment(tx *sql.Tx, actor User, b Booking, i BookingItem, method, reference, key string, now time.Time) (Receipt, error) {
	var e error
	if method == "cash" {
		var next int64
		e = tx.QueryRow("SELECT MAX((SELECT COUNT(*) FROM payments WHERE method='cash'),COALESCE((SELECT MAX(CAST(SUBSTR(reference,6) AS INTEGER)) FROM payments WHERE reference GLOB 'CASH-[0-9]*' AND SUBSTR(reference,6) NOT GLOB '*[^0-9]*'),0))+1").Scan(&next)
		if e != nil {
			return Receipt{}, e
		}
		reference = fmt.Sprintf("CASH-%06d", next)
	}
	result, e := tx.Exec("INSERT INTO payments(booking_id,item_id,amount_pesos,method,reference,cashier_id,payment_key,created_at) VALUES(?,?,?,?,?,?,?,?)", b.ID, i.ID, i.AmountPesos, method, reference, actor.ID, key, stamp(now))
	if e != nil {
		return Receipt{}, ErrConflict
	}
	paymentID, _ := result.LastInsertId()
	if _, e = tx.Exec("UPDATE booking_items SET status='paid' WHERE id=?", i.ID); e != nil {
		return Receipt{}, e
	}
	if i.Kind == "base" {
		_, e = tx.Exec("UPDATE bookings SET status='confirmed' WHERE id=?", b.ID)
	} else {
		_, e = tx.Exec("UPDATE bookings SET end_at=? WHERE id=?", stamp(i.ProposedEnd), b.ID)
	}
	if e != nil {
		return Receipt{}, e
	}
	desc := fmt.Sprintf("%s · %s · %d × 30 min", b.Reference, i.Kind, i.Slots)
	result, e = tx.Exec("INSERT INTO receipts(number,payment_id,booking_id,description,amount_pesos,created_at) VALUES('',?,?,?,?,?)", paymentID, b.ID, desc, i.AmountPesos, stamp(now))
	if e != nil {
		return Receipt{}, e
	}
	receiptID, _ := result.LastInsertId()
	number := fmt.Sprintf("RC-%06d", receiptID)
	if _, e = tx.Exec("UPDATE receipts SET number=? WHERE id=?", number, receiptID); e != nil {
		return Receipt{}, e
	}
	if e = audit(tx, actor.ID, "payment.recorded", paymentID, now); e != nil {
		return Receipt{}, e
	}
	out := Receipt{receiptID, number, paymentID, b.ID, desc, i.AmountPesos, now}
	return out, nil
}
