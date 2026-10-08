package app

import "database/sql"

// collect closes each result before the next query on our single SQLite connection.
func collect[T any](tx *sql.Tx, query string, scan func(interface{ Scan(...any) error }) (T, error), args ...any) ([]T, error) {
	out := []T{}
	rows, e := tx.Query(query, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return out, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Snapshot(actor User) (Snapshot, error) {
	out := Snapshot{Users: []User{}, Customers: []Customer{}, ServerTime: s.now()}
	e := s.transact(func(tx *sql.Tx) error {
		r, e := role(tx, actor)
		if e != nil {
			return e
		}
		if e = expire(tx, out.ServerTime); e != nil {
			return e
		}
		out.User, e = scanUser(tx.QueryRow("SELECT "+userCols+" FROM users WHERE id=?", actor.ID))
		if e != nil {
			return e
		}
		out.PCs, e = collect(tx, "SELECT id,code,tier_id,specs,enabled FROM pcs ORDER BY id", scanPC)
		if e != nil {
			return e
		}
		out.Tiers, e = collect(tx, "SELECT id,name,price_pesos FROM tiers ORDER BY id", func(row interface{ Scan(...any) error }) (Tier, error) {
			var v Tier
			e := row.Scan(&v.ID, &v.Name, &v.PricePesos)
			return v, e
		})
		if e != nil {
			return e
		}
		out.Maintenance, e = collect(tx, "SELECT id,pc_id,start_at,end_at,reason,public_reason FROM maintenance ORDER BY id", func(row interface{ Scan(...any) error }) (Maintenance, error) {
			var v Maintenance
			var a, b int64
			e := row.Scan(&v.ID, &v.PCID, &a, &b, &v.Reason, &v.PublicReason)
			if r == "customer" {
				v.Reason = v.PublicReason
			}
			v.StartAt = at(a)
			v.EndAt = at(b)
			return v, e
		})
		if e != nil {
			return e
		}
		// The predicate is generated only from the authenticated database role.
		bookingFilter := "SELECT id FROM bookings"
		var args []any
		if r == "customer" {
			bookingFilter += " WHERE user_id=? AND id NOT IN (SELECT booking_id FROM walk_ins)"
			args = []any{actor.ID}
		}
		out.Bookings, e = collect(tx, "SELECT "+bookingCols+" FROM bookings WHERE id IN ("+bookingFilter+") ORDER BY id", scanBooking, args...)
		if e != nil {
			return e
		}
		out.NightOffer, e = nightOfferTx(tx)
		if e != nil {
			return e
		}
		out.Reschedules, e = reschedulesTx(tx, bookingFilter, args)
		if e != nil {
			return e
		}
		out.WalkIns, e = collect(tx, "SELECT booking_id,guest_name FROM walk_ins WHERE booking_id IN ("+bookingFilter+") ORDER BY booking_id", func(row interface{ Scan(...any) error }) (WalkIn, error) {
			var v WalkIn
			e := row.Scan(&v.BookingID, &v.GuestName)
			return v, e
		}, args...)
		if e != nil {
			return e
		}
		filter := " WHERE booking_id IN (" + bookingFilter + ") ORDER BY id"
		out.Items, e = collect(tx, "SELECT "+itemCols+" FROM booking_items"+filter, scanItem, args...)
		if e != nil {
			return e
		}
		out.Receipts, e = collect(tx, "SELECT "+receiptCols+" FROM receipts"+filter, scanReceipt, args...)
		if e != nil {
			return e
		}
		out.Payments, e = collect(tx, "SELECT id,booking_id,item_id,amount_pesos,method,reference,cashier_id,created_at FROM payments"+filter, func(row interface{ Scan(...any) error }) (Payment, error) {
			var v Payment
			var n int64
			e := row.Scan(&v.ID, &v.BookingID, &v.ItemID, &v.AmountPesos, &v.Method, &v.Reference, &v.CashierID, &n)
			v.CreatedAt = at(n)
			return v, e
		}, args...)
		if e != nil {
			return e
		}
		out.Plays, e = collect(tx, "SELECT id,booking_id,started_at,ended_at FROM plays"+filter, func(row interface{ Scan(...any) error }) (PlaySession, error) {
			var v PlaySession
			var n int64
			var end sql.NullInt64
			e := row.Scan(&v.ID, &v.BookingID, &n, &end)
			v.StartedAt = at(n)
			v.EndedAt = nullableAt(end)
			return v, e
		}, args...)
		if e != nil {
			return e
		}
		out.Refunds, e = collect(tx, "SELECT id,payment_id,booking_id,amount_pesos,reason,status,requested_by,approved_by,completed_by,created_at,completed_at FROM refunds"+filter, func(row interface{ Scan(...any) error }) (Refund, error) {
			var v Refund
			var n int64
			var end sql.NullInt64
			e := row.Scan(&v.ID, &v.PaymentID, &v.BookingID, &v.AmountPesos, &v.Reason, &v.Status, &v.RequestedBy, &v.ApprovedBy, &v.CompletedBy, &n, &end)
			v.CreatedAt = at(n)
			v.CompletedAt = nullableAt(end)
			return v, e
		}, args...)
		if e != nil {
			return e
		}
		if r == "admin" {
			out.Users, e = collect(tx, "SELECT "+userCols+" FROM users ORDER BY id", scanUser)
			if e != nil {
				return e
			}
		}
		if r == "cashier" || r == "admin" {
			out.Customers, e = collect(tx, "SELECT id,name FROM users WHERE id IN (SELECT user_id FROM bookings WHERE id NOT IN (SELECT booking_id FROM walk_ins)) ORDER BY id", func(row interface{ Scan(...any) error }) (Customer, error) {
				var v Customer
				err := row.Scan(&v.ID, &v.Name)
				return v, err
			})
			if e != nil {
				return e
			}
		}
		auditQuery := "SELECT id,actor_id,action,entity_id,created_at FROM audit"
		var auditArgs []any
		if r == "customer" {
			auditQuery += ` WHERE ((action LIKE 'booking.%' OR action LIKE 'play.%') AND entity_id IN (` + bookingFilter + `)) OR (action LIKE 'payment.%' AND entity_id IN (SELECT id FROM payments WHERE booking_id IN (` + bookingFilter + `))) OR (action LIKE 'extension.%' AND entity_id IN (SELECT id FROM booking_items WHERE booking_id IN (` + bookingFilter + `))) OR (action LIKE 'refund.%' AND entity_id IN (SELECT id FROM refunds WHERE booking_id IN (` + bookingFilter + `))) OR (action LIKE 'user.%' AND entity_id=?)`
			auditArgs = []any{actor.ID, actor.ID, actor.ID, actor.ID, actor.ID}
		}
		out.Audit, e = collect(tx, auditQuery+" ORDER BY id", func(row interface{ Scan(...any) error }) (AuditEvent, error) {
			var v AuditEvent
			var n int64
			e := row.Scan(&v.ID, &v.ActorID, &v.Action, &v.EntityID, &n)
			v.CreatedAt = at(n)
			return v, e
		}, auditArgs...)
		if e != nil {
			return e
		}
		for _, p := range out.Payments {
			out.Report.PaymentsPesos += p.AmountPesos
		}
		for _, f := range out.Refunds {
			if f.Status == "completed" {
				out.Report.RefundsPesos += f.AmountPesos
			}
		}
		out.Report.NetPesos = out.Report.PaymentsPesos - out.Report.RefundsPesos
		out.Report.BookingCount = int64(len(out.Bookings))
		for _, p := range out.Plays {
			if p.EndedAt == nil {
				out.Report.ActiveSessions++
			}
		}
		return nil
	})
	return out, e
}
