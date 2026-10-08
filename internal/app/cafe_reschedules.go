package app

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type RescheduleRequest struct {
	ID                   int64     `json:"id"`
	BookingID            int64     `json:"booking_id"`
	PCID                 int64     `json:"pc_id"`
	StartAt              time.Time `json:"start_at"`
	EndAt                time.Time `json:"end_at"`
	Slots                int       `json:"slots"`
	OldAmountPesos       Pesos     `json:"old_amount_pesos"`
	NewAmountPesos       Pesos     `json:"new_amount_pesos"`
	DifferencePesos      Pesos     `json:"difference_pesos"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	ReplacementBookingID int64     `json:"replacement_booking_id"`
}

const rescheduleCols = "id,booking_id,pc_id,start_at,end_at,slots,old_amount_pesos,new_amount_pesos,status,created_at,COALESCE(replacement_booking_id,0)"

func scanReschedule(row interface{ Scan(...any) error }) (RescheduleRequest, error) {
	var v RescheduleRequest
	var start, end, created int64
	err := row.Scan(&v.ID, &v.BookingID, &v.PCID, &start, &end, &v.Slots, &v.OldAmountPesos, &v.NewAmountPesos, &v.Status, &created, &v.ReplacementBookingID)
	v.StartAt, v.EndAt, v.CreatedAt = at(start), at(end), at(created)
	v.DifferencePesos = v.NewAmountPesos - v.OldAmountPesos
	return v, err
}
func reschedulesTx(tx *sql.Tx, bookingFilter string, args []any) ([]RescheduleRequest, error) {
	return collect(tx, "SELECT "+rescheduleCols+" FROM reschedules WHERE booking_id IN ("+bookingFilter+") ORDER BY id", scanReschedule, args...)
}
func occupiedExcluding(tx *sql.Tx, pcID int64, start, end time.Time, oldBookingID int64) (bool, error) {
	var busy bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM maintenance WHERE pc_id=? AND start_at<? AND end_at>?) OR EXISTS(SELECT 1 FROM booking_slots bs JOIN booking_items i ON i.id=bs.item_id WHERE bs.pc_id=? AND bs.start_at+1800>? AND bs.start_at<? AND i.booking_id<>?)`, pcID, stamp(end), stamp(start), pcID, stamp(start), stamp(end), oldBookingID).Scan(&busy)
	return busy, err
}

// Transfer a conflicting allocation only from the validated original booking.
// All allocations and cancellation commit together; a failed insertion rolls back.
func reserveExcluding(tx *sql.Tx, pcID, itemID int64, start time.Time, slots int, oldBookingID int64) error {
	if oldBookingID == 0 {
		return reserve(tx, pcID, itemID, start, slots)
	}
	for k := 0; k < slots; k++ {
		r, err := tx.Exec(`INSERT INTO booking_slots(pc_id,start_at,item_id) VALUES(?,?,?) ON CONFLICT(pc_id,start_at) DO UPDATE SET item_id=excluded.item_id WHERE booking_slots.item_id IN (SELECT id FROM booking_items WHERE booking_id=?)`, pcID, stamp(start.Add(time.Duration(k)*slot)), itemID, oldBookingID)
		if err != nil {
			return ErrConflict
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
	}
	return nil
}

func reschedulable(tx *sql.Tx, b Booking, now time.Time) error {
	if (b.Status != "held" && b.Status != "confirmed") || !now.Before(b.StartAt) {
		return ErrConflict
	}
	var prohibited bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM plays WHERE booking_id=?) OR EXISTS(SELECT 1 FROM booking_items WHERE booking_id=? AND kind='extension') OR EXISTS(SELECT 1 FROM refunds WHERE booking_id=? AND status IN ('pending','approved'))`, b.ID, b.ID, b.ID).Scan(&prohibited)
	if err != nil {
		return err
	}
	if prohibited {
		return ErrConflict
	}
	return nil
}
func rescheduleQuote(tx *sql.Tx, pcID int64, start time.Time, slots int) (Pesos, Pesos, error) {
	var enabled bool
	var unit Pesos
	err := tx.QueryRow("SELECT p.enabled,t.price_pesos FROM pcs p JOIN tiers t ON t.id=p.tier_id WHERE p.id=?", pcID).Scan(&enabled, &unit)
	if err != nil {
		return 0, 0, cafeNotFound(err)
	}
	if !enabled {
		return 0, 0, ErrConflict
	}
	amount, err := sessionPriceTx(tx, unit, start, slots)
	return unit, amount, err
}

func (s *Store) RequestReschedule(actor User, bookingID, pcID int64, start time.Time, slots int, key string) (RescheduleRequest, error) {
	var out RescheduleRequest
	now := s.now()
	start = start.UTC()
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 || slots < 1 || slots > 16 {
		return out, ErrInvalid
	}
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "customer", "cashier", "admin"); err != nil {
			return err
		}
		if err := bookingAccess(tx, actor, bookingID); err != nil {
			return err
		}
		old, err := scanReschedule(tx.QueryRow("SELECT "+rescheduleCols+" FROM reschedules WHERE request_key=?", key))
		if err == nil {
			var requester int64
			if err = tx.QueryRow("SELECT requested_by FROM reschedules WHERE id=?", old.ID).Scan(&requester); err != nil {
				return err
			}
			if requester != actor.ID || old.BookingID != bookingID || old.PCID != pcID || !old.StartAt.Equal(start) || old.Slots != slots {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !validSlot(start, slots, now) {
			return ErrInvalid
		}
		if err = expire(tx, now); err != nil {
			return err
		}
		b, err := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", bookingID))
		if err != nil {
			return cafeNotFound(err)
		}
		if err = reschedulable(tx, b, now); err != nil {
			return err
		}
		if pcID == b.PCID && start.Equal(b.StartAt) && start.Add(time.Duration(slots)*slot).Equal(b.EndAt) {
			return ErrInvalid
		}
		busy, err := occupiedExcluding(tx, pcID, start, start.Add(time.Duration(slots)*slot), bookingID)
		if err != nil {
			return err
		}
		if busy {
			return ErrConflict
		}
		unit, amount, err := rescheduleQuote(tx, pcID, start, slots)
		if err != nil {
			return err
		}
		var oldAmount Pesos
		if err = tx.QueryRow("SELECT amount_pesos FROM booking_items WHERE booking_id=? AND kind='base'", bookingID).Scan(&oldAmount); err != nil {
			return err
		}
		r, err := tx.Exec(`INSERT INTO reschedules(booking_id,pc_id,start_at,end_at,slots,old_amount_pesos,new_amount_pesos,unit_pesos,status,created_at,request_key,requested_by) VALUES(?,?,?,?,?,?,?,?,'pending',?,?,?)`, bookingID, pcID, stamp(start), stamp(start.Add(time.Duration(slots)*slot)), slots, oldAmount, amount, unit, stamp(now), key, actor.ID)
		if err != nil {
			return ErrConflict
		}
		id, _ := r.LastInsertId()
		if err = audit(tx, actor.ID, "reschedule.requested", id, now); err != nil {
			return err
		}
		out, err = scanReschedule(tx.QueryRow("SELECT "+rescheduleCols+" FROM reschedules WHERE id=?", id))
		return err
	})
	return out, err
}

func (s *Store) DecideReschedule(actor User, id int64, action string) (Booking, error) {
	var out Booking
	now := s.now()
	if action != "approve" && action != "reject" {
		return out, ErrInvalid
	}
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		proposal, err := scanReschedule(tx.QueryRow("SELECT "+rescheduleCols+" FROM reschedules WHERE id=?", id))
		if err != nil {
			return cafeNotFound(err)
		}
		if proposal.Status != "pending" {
			return ErrConflict
		}
		if action == "reject" {
			if _, err = tx.Exec("UPDATE reschedules SET status='rejected' WHERE id=?", id); err != nil {
				return err
			}
			return audit(tx, actor.ID, "reschedule.rejected", id, now)
		}
		if err = expire(tx, now); err != nil {
			return err
		}
		b, err := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE id=?", proposal.BookingID))
		if err != nil {
			return err
		}
		if err = reschedulable(tx, b, now); err != nil {
			return err
		}
		if !validSlot(proposal.StartAt, proposal.Slots, now) {
			return ErrConflict
		}
		unit, amount, err := rescheduleQuote(tx, proposal.PCID, proposal.StartAt, proposal.Slots)
		if err != nil {
			return err
		}
		var quotedUnit, oldAmount Pesos
		if err = tx.QueryRow("SELECT unit_pesos FROM reschedules WHERE id=?", id).Scan(&quotedUnit); err != nil {
			return err
		}
		if err = tx.QueryRow("SELECT amount_pesos FROM booking_items WHERE booking_id=? AND kind='base'", b.ID).Scan(&oldAmount); err != nil {
			return err
		}
		if unit != quotedUnit || amount != proposal.NewAmountPesos || oldAmount != proposal.OldAmountPesos {
			return ErrConflict
		}
		var guest string
		err = tx.QueryRow("SELECT guest_name FROM walk_ins WHERE booking_id=?", b.ID).Scan(&guest)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out, err = createReservationExcluding(tx, actor, proposal.PCID, proposal.StartAt, proposal.Slots, "reschedule:"+token(), guest, now, b.ID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE bookings SET user_id=? WHERE id=?", b.UserID, out.ID); err != nil {
			return err
		}
		out.UserID = b.UserID
		if err = cancelBookingTx(tx, actor, b.ID, now); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE reschedules SET status='approved',replacement_booking_id=? WHERE id=?", out.ID, id); err != nil {
			return err
		}
		return audit(tx, actor.ID, "reschedule.approved", id, now)
	})
	if err != nil {
		return Booking{}, err
	}
	return out, nil
}
