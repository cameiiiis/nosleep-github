package app

import "time"

// sessionPrice applies 20% off to billed minutes between 01:00 and 06:00
// Philippine time. Round the final charge once to two decimal places.
// Minute precision also keeps immediate-start quotes stable within a minute.
func sessionPrice(rate Pesos, start time.Time, slots int) Pesos {
	local := start.UTC().Add(8 * time.Hour)
	var discounted Pesos
	for minute := 0; minute < slots*30; minute++ {
		hour := local.Add(time.Duration(minute) * time.Minute).Hour()
		if hour >= 1 && hour < 6 {
			discounted++
		}
	}
	// Split the division to avoid overflowing even at the maximum tier rate.
	// Round savings down on a half-cent so the final charge rounds half up.
	saving := rate/150*discounted + (rate%150*discounted+74)/150
	return rate*Pesos(slots) - saving
}
