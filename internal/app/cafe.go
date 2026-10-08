package app

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type NightOffer struct {
	Enabled   bool `json:"enabled"`
	Percent   int  `json:"percent"`
	StartHour int  `json:"start_hour"`
	EndHour   int  `json:"end_hour"`
}

func DefaultNightOffer() NightOffer { return NightOffer{true, 20, 1, 6} }
func initCafeSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS night_offer(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER NOT NULL,percent INTEGER NOT NULL CHECK(percent BETWEEN 0 AND 80),start_hour INTEGER NOT NULL,end_hour INTEGER NOT NULL,CHECK(start_hour>=0 AND start_hour<end_hour AND end_hour<=24));
 INSERT OR IGNORE INTO night_offer VALUES(1,1,20,1,6);
 CREATE TABLE IF NOT EXISTS reschedules(id INTEGER PRIMARY KEY,booking_id INTEGER NOT NULL REFERENCES bookings(id),pc_id INTEGER NOT NULL REFERENCES pcs(id),start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,slots INTEGER NOT NULL CHECK(slots BETWEEN 1 AND 16),old_amount_pesos TEXT NOT NULL,new_amount_pesos TEXT NOT NULL,unit_pesos TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','approved','rejected')),created_at INTEGER NOT NULL,replacement_booking_id INTEGER REFERENCES bookings(id),request_key TEXT NOT NULL UNIQUE,requested_by INTEGER NOT NULL REFERENCES users(id));
 CREATE UNIQUE INDEX IF NOT EXISTS one_pending_reschedule ON reschedules(booking_id) WHERE status='pending';
 CREATE TABLE IF NOT EXISTS cashier_shifts(id INTEGER PRIMARY KEY,cashier_id INTEGER NOT NULL REFERENCES users(id),opened_at INTEGER NOT NULL,closed_at INTEGER,opening_cash_pesos TEXT NOT NULL,cash_payments_pesos TEXT NOT NULL DEFAULT '0.00',external_payments_pesos TEXT NOT NULL DEFAULT '0.00',cash_refunds_pesos TEXT NOT NULL DEFAULT '0.00',external_refunds_pesos TEXT NOT NULL DEFAULT '0.00',expected_cash_pesos TEXT NOT NULL DEFAULT '0.00',counted_cash_pesos TEXT,difference_pesos TEXT,notes TEXT NOT NULL DEFAULT '',status TEXT NOT NULL CHECK(status IN ('open','closed')),start_payment_id INTEGER NOT NULL,start_audit_id INTEGER NOT NULL,end_payment_id INTEGER,end_audit_id INTEGER);
 CREATE UNIQUE INDEX IF NOT EXISTS one_open_cashier_shift ON cashier_shifts(cashier_id) WHERE status='open';`)
	return err
}
func nightOfferTx(tx *sql.Tx) (NightOffer, error) {
	var o NightOffer
	err := tx.QueryRow("SELECT enabled,percent,start_hour,end_hour FROM night_offer WHERE id=1").Scan(&o.Enabled, &o.Percent, &o.StartHour, &o.EndHour)
	return o, err
}
func (s *Store) SaveNightOffer(actor User, o NightOffer) error {
	if o.Percent < 0 || o.Percent > 80 || o.StartHour < 0 || o.StartHour >= o.EndHour || o.EndHour > 24 {
		return ErrInvalid
	}
	return s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "admin"); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE night_offer SET enabled=?,percent=?,start_hour=?,end_hour=? WHERE id=1", o.Enabled, o.Percent, o.StartHour, o.EndHour); err != nil {
			return err
		}
		return audit(tx, actor.ID, "discount.saved", 1, s.now())
	})
}

func (a *api) registerCafeRoutes(mux *http.ServeMux) {
	a.post(mux, "/api/admin/discount", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var o NightOffer
		if err := decode(r, &o); err != nil {
			return nil, err
		}
		return nil, a.store.SaveNightOffer(u, o)
	})
	a.post(mux, "/api/reschedules", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			BookingID int64     `json:"booking_id"`
			PCID      int64     `json:"pc_id"`
			StartAt   time.Time `json:"start_at"`
			Slots     int       `json:"slots"`
			Key       string    `json:"key"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		return a.store.RequestReschedule(u, v.BookingID, v.PCID, v.StartAt, v.Slots, v.Key)
	})
	a.post(mux, "/api/reschedules/action", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			RescheduleID int64  `json:"reschedule_id"`
			Action       string `json:"action"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		b, err := a.store.DecideReschedule(u, v.RescheduleID, v.Action)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "booking_id": b.ID}, nil
	})
	mux.HandleFunc("GET /api/shifts", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		shifts, current, err := a.store.Shifts(u)
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"shifts": shifts, "current": current})
	})
	a.post(mux, "/api/shifts/open", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			Opening Pesos `json:"opening_cash_pesos"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		return a.store.OpenShift(u, v.Opening)
	})
	a.post(mux, "/api/shifts/close", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			ShiftID int64  `json:"shift_id"`
			Counted Pesos  `json:"counted_cash_pesos"`
			Notes   string `json:"notes"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		return a.store.CloseShift(u, v.ShiftID, v.Counted, v.Notes)
	})
}

func cafeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
