package app

// Guest reservations and immediate sessions with verified payment.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CreateWalkIn reserves a named guest's session without creating a login account.
// Payment remains a separate verified staff action, just as for online bookings.
func (s *Store) CreateWalkIn(actor User, pcID int64, start time.Time, slots int, key, guestName string) (Booking, error) {
	guestName = strings.TrimSpace(guestName)
	if !validGuestName(guestName) {
		return Booking{}, ErrInvalid
	}
	return s.createBooking(actor, pcID, start, slots, key, guestName)
}

func validGuestName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) >= 2 && utf8.RuneCountInString(name) <= 80
}

// StartWalkInNow commits the reservation, verified collection, receipt, and play together.
// Online and scheduled walk-in bookings still use the normal lead and quarter-hour rules.
func (s *Store) StartWalkInNow(actor User, pcID int64, slots int, key, guestName, method, reference string) (Booking, error) {
	return s.startWalkInNow(actor, pcID, slots, key, guestName, method, reference, nil)
}

// StartWalkInNowQuoted verifies the amount staff saw before collecting payment.
func (s *Store) StartWalkInNowQuoted(actor User, pcID int64, slots int, key, guestName, method, reference string, expectedAmount Pesos) (Booking, error) {
	return s.startWalkInNow(actor, pcID, slots, key, guestName, method, reference, &expectedAmount)
}

func (s *Store) startWalkInNow(actor User, pcID int64, slots int, key, guestName, method, reference string, expectedAmount *Pesos) (Booking, error) {
	var out Booking
	key, guestName = strings.TrimSpace(key), strings.TrimSpace(guestName)
	method, reference = strings.TrimSpace(method), strings.TrimSpace(reference)
	if key == "" || slots < 1 || slots > 16 || !validGuestName(guestName) || (method != "cash" && method != "external") || (method == "external" && reference == "") {
		return out, ErrInvalid
	}
	now := s.now().Truncate(time.Second)
	paymentKey := "walk-in-now:" + key
	err := s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		if expectedAmount != nil && *expectedAmount <= 0 {
			return ErrInvalid
		}
		if err := expire(tx, now); err != nil {
			return err
		}
		existing, err := scanBooking(tx.QueryRow("SELECT "+bookingCols+" FROM bookings WHERE checkout_key=?", key))
		if err == nil {
			var name, originalMethod, originalRef, originalPaymentKey string
			var originalSlots, plays int
			var originalAmount Pesos
			err = tx.QueryRow(`SELECT w.guest_name,i.slots,p.method,p.reference,p.payment_key,p.amount_pesos,(SELECT COUNT(*) FROM plays WHERE booking_id=b.id)
 FROM bookings b JOIN walk_ins w ON w.booking_id=b.id JOIN booking_items i ON i.booking_id=b.id AND i.kind='base' JOIN payments p ON p.item_id=i.id WHERE b.id=?`, existing.ID).Scan(&name, &originalSlots, &originalMethod, &originalRef, &originalPaymentKey, &originalAmount, &plays)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			if existing.UserID != actor.ID || existing.PCID != pcID || name != guestName || originalSlots != slots || originalMethod != method || (method == "external" && originalRef != reference) || originalPaymentKey != paymentKey || plays != 1 || !existing.StartAt.Equal(existing.CreatedAt) || (expectedAmount != nil && *expectedAmount != originalAmount) {
				return ErrConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var active bool
		if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM plays p JOIN bookings b ON b.id=p.booking_id WHERE b.pc_id=? AND p.ended_at IS NULL)", pcID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrConflict
		}
		out, err = createReservation(tx, actor, pcID, now, slots, key, guestName, now)
		if err != nil {
			return err
		}
		item, err := scanItem(tx.QueryRow("SELECT "+itemCols+" FROM booking_items WHERE booking_id=? AND kind='base'", out.ID))
		if err != nil {
			return err
		}
		if expectedAmount != nil && *expectedAmount != item.AmountPesos {
			return ErrConflict
		}
		if _, err = recordPayment(tx, actor, out, item, method, reference, paymentKey, now); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO plays(booking_id,started_at) VALUES(?,?)", out.ID, stamp(now)); err != nil {
			return err
		}
		if err = audit(tx, actor.ID, "play.started", out.ID, now); err != nil {
			return err
		}
		out.Status = "confirmed"
		return nil
	})
	if err != nil {
		return Booking{}, err
	}
	return out, nil
}

type WalkInAvailability struct {
	AvailabilityResult
	StartAt time.Time `json:"start_at"`
}

func (s *Store) WalkInAvailability(actor User, slots int) (WalkInAvailability, error) {
	out := WalkInAvailability{StartAt: s.now().Truncate(time.Second)}
	if slots < 1 || slots > 16 {
		return out, ErrInvalid
	}
	if err := s.transact(func(tx *sql.Tx) error { return require(tx, actor, "cashier", "admin") }); err != nil {
		return out, err
	}
	result, err := s.AvailabilityDetails(out.StartAt, out.StartAt.Add(time.Duration(slots)*slot))
	if err != nil {
		return out, err
	}
	err = s.transact(func(tx *sql.Tx) error {
		if err := require(tx, actor, "cashier", "admin"); err != nil {
			return err
		}
		active, err := collect(tx, "SELECT DISTINCT b.pc_id FROM plays p JOIN bookings b ON b.id=p.booking_id WHERE p.ended_at IS NULL", func(row interface{ Scan(...any) error }) (int64, error) {
			var id int64
			err := row.Scan(&id)
			return id, err
		})
		if err != nil {
			return err
		}
		busy := make(map[int64]bool, len(active))
		for _, id := range active {
			busy[id] = true
		}
		available := []PC{}
		for _, pc := range result.PCs {
			if !busy[pc.ID] {
				available = append(available, pc)
			}
		}
		result.PCs = available
		for i := range result.Statuses {
			if busy[result.Statuses[i].PCID] {
				if result.Statuses[i].Status == "available" {
					result.Statuses[i].Status = "reserved"
				}
				result.Statuses[i].NextStart = nil
			}
		}
		out.AvailabilityResult = result
		return nil
	})
	return out, err
}

func (a *api) registerWalkInRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/walk-ins/availability", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			a.unauthorized(w, err)
			return
		}
		slots, err := strconv.Atoi(r.URL.Query().Get("slots"))
		if err != nil {
			a.fail(w, ErrInvalid)
			return
		}
		result, err := a.store.WalkInAvailability(u, slots)
		if err != nil {
			a.fail(w, err)
			return
		}
		reply(w, http.StatusOK, result)
	})
	a.post(mux, "/api/walk-ins/start-now", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			PCID           int64  `json:"pc_id"`
			Slots          int    `json:"slots"`
			Key            string `json:"key"`
			GuestName      string `json:"guest_name"`
			Method         string `json:"method"`
			Reference      string `json:"reference"`
			ExpectedAmount Pesos  `json:"expected_amount_pesos"`
		}
		if err := decode(r, &v); err != nil {
			return nil, err
		}
		return a.store.StartWalkInNowQuoted(u, v.PCID, v.Slots, v.Key, v.GuestName, v.Method, v.Reference, v.ExpectedAmount)
	})
	a.post(mux, "/api/walk-ins", true, func(w http.ResponseWriter, r *http.Request, u User) (any, error) {
		var v struct {
			PCID      int64     `json:"pc_id"`
			StartAt   time.Time `json:"start_at"`
			Slots     int       `json:"slots"`
			Key       string    `json:"key"`
			GuestName string    `json:"guest_name"`
		}
		if e := decode(r, &v); e != nil {
			return nil, e
		}
		return a.store.CreateWalkIn(u, v.PCID, v.StartAt, v.Slots, v.Key, v.GuestName)
	})
}
