package app

// Exact Philippine peso amounts, night discount pricing and old database conversion.

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// --- Exact Philippine peso parsing and formatting ---

// Pesos uses exact fixed-point arithmetic. JSON and SQLite expose PHP amounts
// such as 30.55; the internal integer prevents floating-point rounding errors.
type Pesos int64

func ParsePesos(value string) (Pesos, error) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalid
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, ErrInvalid
		}
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	digits := parts[0] + fraction
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, ErrInvalid
		}
	}
	n, err := strconv.ParseUint(digits, 10, 63)
	if err != nil {
		return 0, ErrInvalid
	}
	out := Pesos(n)
	if negative {
		out = -out
	}
	return out, nil
}

func (p Pesos) String() string {
	digits := strconv.FormatInt(int64(p), 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	for len(digits) < 3 {
		digits = "0" + digits
	}
	return sign + digits[:len(digits)-2] + "." + digits[len(digits)-2:]
}

func (p Pesos) MarshalJSON() ([]byte, error) { return []byte(p.String()), nil }
func (p *Pesos) UnmarshalJSON(data []byte) error {
	value := string(data)
	if strings.HasPrefix(value, "\"") {
		if err := json.Unmarshal(data, &value); err != nil {
			return ErrInvalid
		}
	}
	v, err := ParsePesos(value)
	if err == nil {
		*p = v
	}
	return err
}
func (p Pesos) Value() (driver.Value, error) { return p.String(), nil }
func (p *Pesos) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("expected a Philippine peso decimal, got %T", value)
	}
	v, err := ParsePesos(text)
	if err == nil {
		*p = v
	}
	return err
}

// Sum exact decimal amounts in Go rather than SQLite's floating-point SUM.
func reservedRefundAmount(tx *sql.Tx, paymentID int64) (Pesos, error) {
	rows, err := tx.Query("SELECT amount_pesos FROM refunds WHERE payment_id=? AND status IN ('pending','approved','completed')", paymentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total Pesos
	for rows.Next() {
		var amount Pesos
		if err := rows.Scan(&amount); err != nil {
			return 0, err
		}
		total += amount
	}
	return total, rows.Err()
}

// --- Night discount calculation ---

// sessionPrice applies 20% off to billed minutes between 01:00 and 06:00
// Philippine time. Round the final charge once to two decimal places.
// Minute precision also keeps immediate-start quotes stable within a minute.
func sessionPrice(rate Pesos, start time.Time, slots int) Pesos {
	return sessionPriceWithOffer(rate, start, slots, DefaultNightOffer())
}

func sessionPriceTx(tx *sql.Tx, rate Pesos, start time.Time, slots int) (Pesos, error) {
	offer, err := nightOfferTx(tx)
	if err != nil {
		return 0, err
	}
	return sessionPriceWithOffer(rate, start, slots, offer), nil
}

func sessionPriceWithOffer(rate Pesos, start time.Time, slots int, offer NightOffer) Pesos {
	local := start.UTC().Add(8 * time.Hour)
	var discounted Pesos
	for minute := 0; minute < slots*30; minute++ {
		hour := local.Add(time.Duration(minute) * time.Minute).Hour()
		if offer.Enabled && hour >= offer.StartHour && hour < offer.EndHour {
			discounted++
		}
	}
	// Split the division to avoid overflowing even at the maximum tier rate.
	// Round savings down on a half-cent so the final charge rounds half up.
	weight := discounted * Pesos(offer.Percent)
	saving := rate/3000*weight + (rate%3000*weight+1499)/3000
	amount := rate*Pesos(slots) - saving
	// Keep positive item amounts even when an unusually tiny rate rounds to zero.
	if amount < 1 {
		return 1
	}
	return amount
}

// --- Keep older databases compatible with peso amounts ---

// Convert old databases once, retaining IDs, relationships and original quotes.
// The old column names appear only here for backwards compatibility.
func migratePesos(db *sql.DB, sqlText string) error {
	var legacy bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('tiers') WHERE name='price_centavos')").Scan(&legacy); err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	err := func() error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, table := range []string{"tiers", "booking_items", "payments", "receipts", "refunds"} {
			prefix := "CREATE TABLE IF NOT EXISTS " + table + "("
			var create string
			for _, statement := range strings.Split(sqlText, ";") {
				statement = strings.TrimSpace(statement)
				if strings.HasPrefix(statement, prefix) {
					create = statement
					break
				}
			}
			if create == "" {
				return fmt.Errorf("missing peso schema for %s", table)
			}
			temp := "peso_migration_" + table
			if _, err := tx.Exec(strings.Replace(create, prefix, "CREATE TABLE "+temp+"(", 1)); err != nil {
				return err
			}
			rows, err := tx.Query("PRAGMA table_info(" + table + ")")
			if err != nil {
				return err
			}
			var columns, values []string
			for rows.Next() {
				var cid, notNull, primaryKey int
				var name, kind string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
					rows.Close()
					return err
				}
				newName, value := name, name
				if strings.HasSuffix(name, "_centavos") {
					newName = strings.TrimSuffix(name, "_centavos") + "_pesos"
					value = "printf('%d.%02d', " + name + "/100, " + name + "%100)"
				}
				columns = append(columns, newName)
				values = append(values, value)
			}
			rowErr := rows.Err()
			rows.Close()
			if rowErr != nil {
				return rowErr
			}
			if _, err := tx.Exec("INSERT INTO " + temp + "(" + strings.Join(columns, ",") + ") SELECT " + strings.Join(values, ",") + " FROM " + table); err != nil {
				return err
			}
			if _, err := tx.Exec("DROP TABLE " + table); err != nil {
				return err
			}
			if _, err := tx.Exec("ALTER TABLE " + temp + " RENAME TO " + table); err != nil {
				return err
			}
		}
		rows, err := tx.Query("PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		broken := rows.Next()
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return rowErr
		}
		if broken {
			return fmt.Errorf("peso migration would break database relationships")
		}
		return tx.Commit()
	}()
	_, restoreErr := db.Exec("PRAGMA foreign_keys=ON")
	if err != nil {
		return err
	}
	return restoreErr
}
