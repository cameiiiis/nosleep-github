package app

import "time"

type User struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
}
type PC struct {
	ID      int64  `json:"id"`
	Code    string `json:"code"`
	TierID  int64  `json:"tier_id"`
	Specs   string `json:"specs"`
	Enabled bool   `json:"enabled"`
}
type Tier struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PricePesos Pesos  `json:"price_pesos"`
}
type Maintenance struct {
	ID           int64     `json:"id"`
	PCID         int64     `json:"pc_id"`
	StartAt      time.Time `json:"start_at"`
	EndAt        time.Time `json:"end_at"`
	Reason       string    `json:"reason"`
	PublicReason string    `json:"public_reason"`
}
type Booking struct {
	ID        int64     `json:"id"`
	Reference string    `json:"reference"`
	UserID    int64     `json:"user_id"`
	PCID      int64     `json:"pc_id"`
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Status    string    `json:"status"`
	HoldUntil time.Time `json:"hold_until"`
	CreatedAt time.Time `json:"created_at"`
}
type BookingItem struct {
	ID          int64     `json:"id"`
	BookingID   int64     `json:"booking_id"`
	Kind        string    `json:"kind"`
	Slots       int       `json:"slots"`
	UnitPesos   Pesos     `json:"unit_pesos"`
	AmountPesos Pesos     `json:"amount_pesos"`
	Status      string    `json:"status"`
	HoldUntil   time.Time `json:"hold_until"`
	ExpectedEnd time.Time `json:"expected_end"`
	ProposedEnd time.Time `json:"proposed_end"`
}
type WalkIn struct {
	BookingID int64  `json:"booking_id"`
	GuestName string `json:"guest_name"`
}
type Payment struct {
	ID          int64     `json:"id"`
	BookingID   int64     `json:"booking_id"`
	ItemID      int64     `json:"item_id"`
	AmountPesos Pesos     `json:"amount_pesos"`
	Method      string    `json:"method"`
	Reference   string    `json:"reference"`
	CashierID   int64     `json:"cashier_id"`
	CreatedAt   time.Time `json:"created_at"`
}
type Receipt struct {
	ID          int64     `json:"id"`
	Number      string    `json:"number"`
	PaymentID   int64     `json:"payment_id"`
	BookingID   int64     `json:"booking_id"`
	Description string    `json:"description"`
	AmountPesos Pesos     `json:"amount_pesos"`
	CreatedAt   time.Time `json:"created_at"`
}
type PlaySession struct {
	ID        int64      `json:"id"`
	BookingID int64      `json:"booking_id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}
type Refund struct {
	ID          int64      `json:"id"`
	PaymentID   int64      `json:"payment_id"`
	BookingID   int64      `json:"booking_id"`
	AmountPesos Pesos      `json:"amount_pesos"`
	Reason      string     `json:"reason"`
	Status      string     `json:"status"`
	RequestedBy int64      `json:"requested_by"`
	ApprovedBy  int64      `json:"approved_by"`
	CompletedBy int64      `json:"completed_by"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}
type AuditEvent struct {
	ID        int64     `json:"id"`
	ActorID   int64     `json:"actor_id"`
	Action    string    `json:"action"`
	EntityID  int64     `json:"entity_id"`
	CreatedAt time.Time `json:"created_at"`
}
type Report struct {
	PaymentsPesos  Pesos `json:"payments_pesos"`
	RefundsPesos   Pesos `json:"refunds_pesos"`
	NetPesos       Pesos `json:"net_pesos"`
	BookingCount   int64 `json:"booking_count"`
	ActiveSessions int64 `json:"active_sessions"`
}
type Snapshot struct {
	User        User          `json:"user"`
	PCs         []PC          `json:"pcs"`
	Tiers       []Tier        `json:"tiers"`
	Maintenance []Maintenance `json:"maintenance"`
	Bookings    []Booking     `json:"bookings"`
	WalkIns     []WalkIn      `json:"walk_ins"`
	Items       []BookingItem `json:"items"`
	Payments    []Payment     `json:"payments"`
	Receipts    []Receipt     `json:"receipts"`
	Plays       []PlaySession `json:"plays"`
	Refunds     []Refund      `json:"refunds"`
	Users       []User        `json:"users"`
	Customers   []Customer    `json:"customers"`
	Audit       []AuditEvent  `json:"audit"`
	Report      Report        `json:"report"`
	ServerTime  time.Time     `json:"server_time"`
}

type Customer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
