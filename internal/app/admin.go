package app

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
)

func (s *Store) SetUserRole(actorID, userID int64, newRole string) error {
	if newRole != "customer" && newRole != "cashier" && newRole != "admin" {
		return ErrInvalid
	}
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, User{ID: actorID}, "admin"); e != nil {
			return e
		}
		var prior string
		var active bool
		e := tx.QueryRow("SELECT role,active FROM users WHERE id=?", userID).Scan(&prior, &active)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if prior == "admin" && active && newRole != "admin" {
			var n int
			e = tx.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND active=1").Scan(&n)
			if e != nil {
				return e
			}
			if n <= 1 {
				return ErrConflict
			}
		}
		if _, e = tx.Exec("UPDATE users SET role=? WHERE id=?", newRole, userID); e != nil {
			return e
		}
		return audit(tx, actorID, "user.role", userID, s.now())
	})
}
func (s *Store) SetUserActive(actorID, userID int64, active bool) error {
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, User{ID: actorID}, "admin"); e != nil {
			return e
		}
		var prior string
		var wasActive bool
		e := tx.QueryRow("SELECT role,active FROM users WHERE id=?", userID).Scan(&prior, &wasActive)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if prior == "admin" && wasActive && !active {
			var n int
			e = tx.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND active=1").Scan(&n)
			if e != nil {
				return e
			}
			if n <= 1 {
				return ErrConflict
			}
		}
		if _, e = tx.Exec("UPDATE users SET active=? WHERE id=?", active, userID); e != nil {
			return e
		}
		if !active {
			if _, e = tx.Exec("DELETE FROM auth_sessions WHERE user_id=?", userID); e != nil {
				return e
			}
		}
		return audit(tx, actorID, "user.active", userID, s.now())
	})
}
func (s *Store) SavePC(actor User, pc PC) error {
	pc.Code = strings.TrimSpace(pc.Code)
	if pc.Code == "" || pc.TierID <= 0 {
		return ErrInvalid
	}
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "admin"); e != nil {
			return e
		}
		if e := expire(tx, s.now()); e != nil {
			return e
		}
		var tier int
		e := tx.QueryRow("SELECT COUNT(*) FROM tiers WHERE id=?", pc.TierID).Scan(&tier)
		if e != nil {
			return e
		}
		if tier == 0 {
			return ErrNotFound
		}
		if pc.ID == 0 {
			r, e := tx.Exec("INSERT INTO pcs(code,tier_id,specs,enabled) VALUES(?,?,?,?)", pc.Code, pc.TierID, pc.Specs, pc.Enabled)
			if e != nil {
				return ErrConflict
			}
			id, _ := r.LastInsertId()
			return audit(tx, actor.ID, "pc.created", id, s.now())
		}
		if !pc.Enabled {
			var n int
			e = tx.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE pc_id=? AND start_at+1800>?", pc.ID, stamp(s.now())).Scan(&n)
			if e != nil {
				return e
			}
			if n > 0 {
				return ErrConflict
			}
		}
		r, e := tx.Exec("UPDATE pcs SET code=?,tier_id=?,specs=?,enabled=? WHERE id=?", pc.Code, pc.TierID, pc.Specs, pc.Enabled, pc.ID)
		if e != nil {
			return ErrConflict
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return audit(tx, actor.ID, "pc.saved", pc.ID, s.now())
	})
}
func (s *Store) SaveTier(actor User, tier Tier) error {
	tier.Name = strings.TrimSpace(tier.Name)
	if tier.Name == "" || tier.PricePesos <= 0 || tier.PricePesos > math.MaxInt64/16 {
		return ErrInvalid
	}
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "admin"); e != nil {
			return e
		}
		if tier.ID == 0 {
			r, e := tx.Exec("INSERT INTO tiers(name,price_pesos) VALUES(?,?)", tier.Name, tier.PricePesos)
			if e != nil {
				return ErrConflict
			}
			id, _ := r.LastInsertId()
			return audit(tx, actor.ID, "tier.created", id, s.now())
		}
		r, e := tx.Exec("UPDATE tiers SET name=?,price_pesos=? WHERE id=?", tier.Name, tier.PricePesos, tier.ID)
		if e != nil {
			return ErrConflict
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return audit(tx, actor.ID, "tier.saved", tier.ID, s.now())
	})
}
func (s *Store) SetMaintenance(actor User, pcID int64, start, end time.Time, reason string) error {
	return s.SetMaintenanceDetails(actor, pcID, start, end, reason, "")
}

func (s *Store) SetMaintenanceDetails(actor User, pcID int64, start, end time.Time, reason, publicReason string) error {
	start = start.UTC()
	end = end.UTC()
	reason = strings.TrimSpace(reason)
	publicReason = strings.TrimSpace(publicReason)
	if !validInterval(start, end) || reason == "" {
		return ErrInvalid
	}
	return s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "admin"); e != nil {
			return e
		}
		if e := expire(tx, s.now()); e != nil {
			return e
		}
		var exists int
		e := tx.QueryRow("SELECT COUNT(*) FROM pcs WHERE id=?", pcID).Scan(&exists)
		if e != nil {
			return e
		}
		if exists == 0 {
			return ErrNotFound
		}
		var n int
		e = tx.QueryRow("SELECT COUNT(*) FROM booking_slots WHERE pc_id=? AND start_at<? AND start_at+1800>?", pcID, stamp(end), stamp(start)).Scan(&n)
		if e != nil {
			return e
		}
		if n > 0 {
			return ErrConflict
		}
		r, e := tx.Exec("INSERT INTO maintenance(pc_id,start_at,end_at,reason,public_reason) VALUES(?,?,?,?,?)", pcID, stamp(start), stamp(end), reason, publicReason)
		if e != nil {
			return e
		}
		id, _ := r.LastInsertId()
		return audit(tx, actor.ID, "maintenance.created", id, s.now())
	})
}
func (s *Store) RequestRefund(actor User, paymentID int64, reason string) (Refund, error) {
	var out Refund
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return out, ErrInvalid
	}
	now := s.now()
	e := s.transact(func(tx *sql.Tx) error {
		if e := require(tx, actor, "customer", "cashier", "admin"); e != nil {
			return e
		}
		var bookingID, owner int64
		var amount Pesos
		e := tx.QueryRow("SELECT p.booking_id,p.amount_pesos,b.user_id FROM payments p JOIN bookings b ON b.id=p.booking_id WHERE p.id=?", paymentID).Scan(&bookingID, &amount, &owner)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if e = bookingAccess(tx, actor, bookingID); e != nil {
			return e
		}
		reserved, e := reservedRefundAmount(tx, paymentID)
		if e != nil {
			return e
		}
		if amount-reserved <= 0 {
			return ErrConflict
		}
		amount -= reserved
		result, e := tx.Exec("INSERT INTO refunds(payment_id,booking_id,amount_pesos,reason,status,requested_by,created_at) VALUES(?,?,?,?,'pending',?,?)", paymentID, bookingID, amount, reason, actor.ID, stamp(now))
		if e != nil {
			return e
		}
		id, _ := result.LastInsertId()
		if e = audit(tx, actor.ID, "refund.requested", id, now); e != nil {
			return e
		}
		out = Refund{ID: id, PaymentID: paymentID, BookingID: bookingID, AmountPesos: amount, Reason: reason, Status: "pending", RequestedBy: actor.ID, CreatedAt: now}
		return nil
	})
	return out, e
}
func (s *Store) DecideRefund(actor User, refundID int64, action string) error {
	now := s.now()
	return s.transact(func(tx *sql.Tx) error {
		var needed string
		switch action {
		case "approve", "reject":
			needed = "admin"
		case "complete":
			needed = "cashier"
		default:
			return ErrInvalid
		}
		if action == "complete" {
			if e := require(tx, actor, "cashier", "admin"); e != nil {
				return e
			}
		} else {
			if e := require(tx, actor, needed); e != nil {
				return e
			}
		}
		var status string
		e := tx.QueryRow("SELECT status FROM refunds WHERE id=?", refundID).Scan(&status)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		switch action {
		case "approve":
			if status != "pending" {
				return ErrConflict
			}
			_, e = tx.Exec("UPDATE refunds SET status='approved',approved_by=? WHERE id=?", actor.ID, refundID)
		case "reject":
			if status != "pending" {
				return ErrConflict
			}
			_, e = tx.Exec("UPDATE refunds SET status='rejected',approved_by=? WHERE id=?", actor.ID, refundID)
		case "complete":
			if status != "approved" {
				return ErrConflict
			}
			_, e = tx.Exec("UPDATE refunds SET status='completed',completed_by=?,completed_at=? WHERE id=?", actor.ID, stamp(now), refundID)
		}
		if e != nil {
			return e
		}
		return audit(tx, actor.ID, "refund."+action, refundID, now)
	})
}
