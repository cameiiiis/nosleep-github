package app

import (
	"database/sql"
	"time"
)

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
