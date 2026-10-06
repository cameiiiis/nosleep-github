package app

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
