package app

import (
	"database/sql"
	"math"
	"strings"
	"time"
)

type CashierShift struct {
	ID                    int64      `json:"id"`
	CashierID             int64      `json:"cashier_id"`
	OpenedAt              time.Time  `json:"opened_at"`
	ClosedAt              *time.Time `json:"closed_at"`
	OpeningCashPesos      Pesos      `json:"opening_cash_pesos"`
	CashPaymentsPesos     Pesos      `json:"cash_payments_pesos"`
	ExternalPaymentsPesos Pesos      `json:"external_payments_pesos"`
	CashRefundsPesos      Pesos      `json:"cash_refunds_pesos"`
	ExternalRefundsPesos  Pesos      `json:"external_refunds_pesos"`
	ExpectedCashPesos     Pesos      `json:"expected_cash_pesos"`
	CountedCashPesos      *Pesos     `json:"counted_cash_pesos"`
	DifferencePesos       *Pesos     `json:"difference_pesos"`
	Notes                 string     `json:"notes"`
	Status                string     `json:"status"`
}

const shiftCols = "id,cashier_id,opened_at,closed_at,opening_cash_pesos,cash_payments_pesos,external_payments_pesos,cash_refunds_pesos,external_refunds_pesos,expected_cash_pesos,counted_cash_pesos,difference_pesos,notes,status"

func scanShift(row interface{ Scan(...any) error }) (CashierShift, error) {
	var v CashierShift
	var opened int64
	var closed sql.NullInt64
	var counted, difference sql.NullString
	err := row.Scan(&v.ID, &v.CashierID, &opened, &closed, &v.OpeningCashPesos, &v.CashPaymentsPesos, &v.ExternalPaymentsPesos, &v.CashRefundsPesos, &v.ExternalRefundsPesos, &v.ExpectedCashPesos, &counted, &difference, &v.Notes, &v.Status)
	if err != nil {
		return v, err
	}
	v.OpenedAt = at(opened)
	v.ClosedAt = nullableAt(closed)
	if counted.Valid {
		p, e := ParsePesos(counted.String)
		if e != nil {
			return v, e
		}
		v.CountedCashPesos = &p
	}
	if difference.Valid {
		p, e := ParsePesos(difference.String)
		if e != nil {
			return v, e
		}
		v.DifferencePesos = &p
	}
	return v, nil
}
func addShiftMoney(total *Pesos, amount Pesos) error {
	if amount < 0 || *total > Pesos(math.MaxInt64)-amount {
		return ErrConflict
	}
	*total += amount
	return nil
}
func shiftBoundaries(tx *sql.Tx) (int64, int64, error) {
	var payment, auditID int64
	err := tx.QueryRow("SELECT COALESCE((SELECT MAX(id) FROM payments),0),COALESCE((SELECT MAX(id) FROM audit),0)").Scan(&payment, &auditID)
	return payment, auditID, err
}
func shiftTotalsTx(tx *sql.Tx, v CashierShift, endPayment, endAudit int64) (CashierShift, error) {
	var startPayment, startAudit int64
	err := tx.QueryRow("SELECT start_payment_id,start_audit_id FROM cashier_shifts WHERE id=?", v.ID).Scan(&startPayment, &startAudit)
	if err != nil {
		return v, err
	}
	v.CashPaymentsPesos, v.ExternalPaymentsPesos, v.CashRefundsPesos, v.ExternalRefundsPesos = 0, 0, 0, 0
	rows, err := tx.Query("SELECT method,amount_pesos FROM payments WHERE cashier_id=? AND id>? AND id<=?", v.CashierID, startPayment, endPayment)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var method string
		var amount Pesos
		if err = rows.Scan(&method, &amount); err != nil {
			break
		}
		total := &v.ExternalPaymentsPesos
		if method == "cash" {
			total = &v.CashPaymentsPesos
		}
		if err = addShiftMoney(total, amount); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = tx.Query(`SELECT p.method,r.amount_pesos FROM refunds r JOIN payments p ON p.id=r.payment_id WHERE r.status='completed' AND r.completed_by=? AND EXISTS(SELECT 1 FROM audit a WHERE a.action='refund.complete' AND a.entity_id=r.id AND a.id>? AND a.id<=?)`, v.CashierID, startAudit, endAudit)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var method string
		var amount Pesos
		if err = rows.Scan(&method, &amount); err != nil {
			break
		}
		total := &v.ExternalRefundsPesos
		if method == "cash" {
			total = &v.CashRefundsPesos
		}
		if err = addShiftMoney(total, amount); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return v, err
	}
	expected := v.OpeningCashPesos
	if err = addShiftMoney(&expected, v.CashPaymentsPesos); err != nil {
		return v, err
	}
	v.ExpectedCashPesos = expected - v.CashRefundsPesos
	return v, nil
}

func (s *Store) Shifts(actor User) ([]CashierShift, *CashierShift, error) {
	out := []CashierShift{}
	var current *CashierShift
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		r, err := role(tx, actor)
		if err != nil {
			return err
		}
		query := "SELECT " + shiftCols + " FROM cashier_shifts"
		var args []any
		if r != "admin" {
			query += " WHERE cashier_id=?"
			args = []any{actor.ID}
		}
		out, err = collect(tx, query+" ORDER BY id DESC", scanShift, args...)
		if err != nil {
			return err
		}
		payment, auditID, err := shiftBoundaries(tx)
		if err != nil {
			return err
		}
		for i := range out {
			if out[i].Status != "open" {
				continue
			}
			out[i], err = shiftTotalsTx(tx, out[i], payment, auditID)
			if err != nil {
				return err
			}
			if out[i].CashierID == actor.ID && current == nil {
				v := out[i]
				current = &v
			}
		}
		return nil
	})
	return out, current, err
}

func (s *Store) OpenShift(actor User, opening Pesos) (CashierShift, error) {
	var out CashierShift
	if opening < 0 {
		return out, ErrInvalid
	}
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		payment, auditID, err := shiftBoundaries(tx)
		if err != nil {
			return err
		}
		r, err := tx.Exec(`INSERT INTO cashier_shifts(cashier_id,opened_at,opening_cash_pesos,expected_cash_pesos,status,start_payment_id,start_audit_id) VALUES(?,?,?,?,'open',?,?)`, actor.ID, stamp(s.now()), opening, opening, payment, auditID)
		if err != nil {
			return ErrConflict
		}
		id, _ := r.LastInsertId()
		if err = audit(tx, actor.ID, "shift.opened", id, s.now()); err != nil {
			return err
		}
		out, err = scanShift(tx.QueryRow("SELECT "+shiftCols+" FROM cashier_shifts WHERE id=?", id))
		return err
	})
	return out, err
}

func (s *Store) CloseShift(actor User, id int64, counted Pesos, notes string) (CashierShift, error) {
	var out CashierShift
	notes = strings.TrimSpace(notes)
	if counted < 0 || len(notes) > 2000 {
		return out, ErrInvalid
	}
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		v, err := scanShift(tx.QueryRow("SELECT "+shiftCols+" FROM cashier_shifts WHERE id=?", id))
		if err != nil {
			return cafeNotFound(err)
		}
		r, err := role(tx, actor)
		if err != nil {
			return err
		}
		if actor.ID != v.CashierID && r != "admin" {
			return ErrForbidden
		}
		if v.Status != "open" {
			return ErrConflict
		}
		payment, auditID, err := shiftBoundaries(tx)
		if err != nil {
			return err
		}
		v, err = shiftTotalsTx(tx, v, payment, auditID)
		if err != nil {
			return err
		}
		// A negative expected balance plus a very large count can overflow a difference.
		if v.ExpectedCashPesos < 0 && counted > Pesos(math.MaxInt64)+v.ExpectedCashPesos {
			return ErrInvalid
		}
		difference := counted - v.ExpectedCashPesos
		_, err = tx.Exec(`UPDATE cashier_shifts SET closed_at=?,cash_payments_pesos=?,external_payments_pesos=?,cash_refunds_pesos=?,external_refunds_pesos=?,expected_cash_pesos=?,counted_cash_pesos=?,difference_pesos=?,notes=?,status='closed',end_payment_id=?,end_audit_id=? WHERE id=?`, stamp(s.now()), v.CashPaymentsPesos, v.ExternalPaymentsPesos, v.CashRefundsPesos, v.ExternalRefundsPesos, v.ExpectedCashPesos, counted, difference, notes, payment, auditID, id)
		if err != nil {
			return err
		}
		if err = audit(tx, actor.ID, "shift.closed", id, s.now()); err != nil {
			return err
		}
		out, err = scanShift(tx.QueryRow("SELECT "+shiftCols+" FROM cashier_shifts WHERE id=?", id))
		return err
	})
	return out, err
}
