# NO SLEEP

A student gaming café system using **Go, SQLite, HTML, CSS and JavaScript**.
Players reserve PCs. Staff record payments, check players in and handle refunds.
The website records transactions; it does not transfer money or control physical PCs.

## Run in VS Code

1. Install **Go 1.26 or newer** and VS Code.
2. Open **nosleep-github**, the folder containing `main.go` and `go.mod`.
3. Open **Terminal → New Terminal** and run:

   ```powershell
   go run .
   ```

4. Open **http://127.0.0.1:8080** in your browser. Keep the terminal running.
5. Press **Ctrl+C in that terminal** to stop.
6. After saving code edits, stop Go, run `go run .` again and refresh the browser.

The first run downloads Go dependencies. You do not need npm, Live Server,
XAMPP, MySQL or a separate database server.

Go runs the backend in your terminal. The browser displays the frontend.
JavaScript sends requests to Go, and Go reads/saves records in SQLite.

## Demo accounts and roles

| Role | Email | Password | Main functions |
| --- | --- | --- | --- |
| Player | `player@nosleep.local` | `Player123!` | Book PCs, view history/receipts/refunds, request extensions/reschedules and password recovery |
| Cashier | `cashier@nosleep.local` | `Cashier123!` | Record verified payments, create walk-ins, start/end play, complete approved refunds, review reschedules, use reports/ledger/shifts |
| Admin | `admin@nosleep.local` | `Admin123!` | Cashier functions plus manage PCs/rates/users/maintenance, decide refunds, approve recovery, configure discounts and inspect audit/backups/shifts |

These are local demo accounts. Existing changed passwords and disabled accounts
are preserved. Use different browser profiles to test player and staff together;
tabs in the same browser profile share a login.

## Main flow: booking → payment → play

1. Player chooses date, start time, duration and station in **Book a PC**.
2. Review the total and confirm **Place 10-minute hold**.
3. Pay the cashier while the slot is held.
4. Cashier verifies the actual cash/external transfer and records payment.
5. The booking is confirmed and a receipt is issued. Cash references are numbered
   automatically; external transfers need their actual reference.
6. Cashier starts play at check-in and ends it when finished.
7. Players find past bookings, receipts and refund states in **History**.

Unpaid holds expire and release their slots. Staff can create a **Walk-in** for
later or use **Start now** after checking availability and verifying payment.
Guests do not need player accounts.

### Time and prices

- Dates/times use **Philippine time (UTC+8)**; amounts use **Philippine pesos**.
- Scheduled starts are at **:00, :15, :30 or :45**, at least **15 minutes ahead**.
- Reserve hours or days ahead, up to **30 days**.
- Duration uses **30-minute blocks**, from 30 minutes to 8 hours.
- Availability checks the whole duration, reservations and maintenance.
- Default Standard/Pro/Elite rates: **₱30/₱45/₱60 per 30 minutes**.
- Default night offer: **20% off from 1 AM to 6 AM** on all PCs. Only minutes
  within that window are discounted. Admin can change/disable the offer.
- Existing charges keep their original prices when settings change.

A Standard PC for 60 regular minutes costs **₱60**; all 60 minutes within the
default night offer cost **₱48**.

## Cancellation and refund

1. Cancel an eligible paid booking before play. This creates pending refunds for
   remaining refundable payments; unpaid holds need no refund.
2. Admin approves or rejects the request in **Refunds**.
3. Staff actually return the money outside the app.
4. Cashier/admin records **Complete refund** only after returning it.
5. Player History shows **Refunded**, the amount and completion time.

Approval alone is not money return. Original payments/receipts stay saved.
Staff can request other eligible payment refunds through Details. Reports deduct
completed refunds, not pending approvals.

## Password recovery: admin approval required

1. Player selects **Forgot password?** and enters their email.
2. Admin opens **Password recovery**, verifies identity and confirms
   **Identity verified · issue code**.
3. Admin privately gives the approved code to the verified owner.
4. Player selects **Enter reset code**, enters it and chooses a new password.

Codes expire after **15 minutes** and work **once**. Players/cashiers cannot issue
them. Forgot password alone never generates/emails a code. Reset/change signs
the account out of every session. **Account → Change password** requires the
current password. Passwords must be 8–72 bytes. Optional SMTP delivers codes
only after admin approval; local recovery works without email configuration.

## Other features

| Feature | Function |
| --- | --- |
| Extensions | Request extra time; server checks availability, holds the charge and staff verify payment |
| Reschedules | Staff-approved replacement cancels the original, creates its refunds and places a new hold requiring separate payment |
| Ledger/history search | Find records by player/reference/station/status/date |
| Reports/CSV | Payment recording dates and refund completion dates use Philippine calendar days |
| Cashier shifts | Opening cash + own cash collections − own completed cash refunds = expected cash; closing saves counted cash and difference |
| Maintenance | Block a station interval; private notes and public explanations are separate |
| Reminders | Approaching hold expiry/play end while the page is open |
| Quiet Mode | Simplify the active-session display without changing billing/time |
| Connection recovery | Show errors when needed; prevent offline writes and never automatically repeat financial actions |
| Backups | Startup/hourly consistent snapshots, seven retained; admin create/download and offline restore |

Reschedules require a future held/confirmed booking with no play, extensions or
pending/approved refund. Proposals do not reserve a second slot. Shifts are
optional; transactions outside them are not included in a later shift.

## Files: what should I study first?

Start with the first six rows. You do not need to study every file at once.

| File | Purpose |
| --- | --- |
| `main.go` | Starts the server/database/frontend and stops with Ctrl+C |
| `web/index.html` | HTML shell and shared confirmation dialog |
| `web/styles.css` | Dark design, spacing, responsiveness and printing |
| `web/app.js` | Browser pages, forms, buttons and requests to Go |
| `internal/app/routes.go` | Main HTTP routes, login/register, JSON and request checks |
| `internal/app/bookings.go` | Holds, payments/receipts, cancellation, play and extensions |
| `internal/app/database.go` | SQLite, defaults, transactions, starter copy and saved database choice |
| `internal/app/schema.sql` | Main tables and relationships |
| `internal/app/models.go` | Structures such as User, PC, Booking and Payment |
| `internal/app/snapshot.go` | Collect the data each role may see |
| `internal/app/accounts.go` | Password/recovery, attempt limits and optional approved-code email |
| `internal/app/money.go` | Exact peso calculations, discount pricing and older database conversion |
| `internal/app/admin.go` | PCs/users/rates/maintenance/refunds and extra feature setup/routes |
| `internal/app/walkins.go` | Guest bookings and Start now |
| `internal/app/reports.go` | Availability explanations, reports/CSV, reference checks and immediate backup download |
| `internal/app/reschedules.go` | Staff-reviewed replacement bookings |
| `internal/app/shifts.go` | Cashier opening/closing and drawer totals |
| `internal/app/backups.go` | Automatic backups, downloads and offline restore |
| `*_test.go`, `tests/*.test.cjs` | Automated checks; you do not edit/run these for normal app use |
| `go.mod`, `go.sum` | Go version/dependencies and dependency checksums |
| `.gitignore` | Exclude local working data, caches, binaries and secrets from Git |

Related code is grouped by topic with section comments. All Go files inside
`internal/app` belong to the same package. You do not run them separately:
**`go run .` starts the whole program.**

For classroom explanations: browser button → JavaScript `fetch` → Go route →
SQLite transaction → JSON response → updated page. Transactions commit all
related changes together or roll them back, such as payment + receipt + booking
confirmation. Money, permissions and slot checks remain on the backend.

## Database and teammate handoff

SQLite is a local file. Its **absolute active path is printed at startup**.

| Path | Purpose |
| --- | --- |
| `data/nosleep.db` | Included starter for the first default runtime copy, or an explicitly selected legacy working file |
| `data/runtime.db` | Normal local working database |
| `data/local-config.json` | Saved database selection |
| `data/backups/` | Retained backups |

Existing data is preserved. Explicit `NO_SLEEP_DB` updates the saved choice for
future terminals. First-run divergent databases are warned about, never merged.
Inspect a backup or use a SQLite viewer read-only. Direct SQL edits bypass app rules.

Push source and the starter to GitHub. Your teammate clones, opens the folder
and runs `go run .`. Each clone has its **own data**; Git does not synchronize
live bookings. Keep private databases/backups/email secrets out of Git.
`.cache` is ignored local build/development data and is unnecessary for handoff.

## Optional configuration

Set variables in the same PowerShell terminal before `go run .`. `.env` files
are not loaded automatically. Normally no settings are needed.

| Variable | Purpose |
| --- | --- |
| `NO_SLEEP_ADDR` | Default `127.0.0.1:8080` |
| `NO_SLEEP_DB` | Explicit database choice, saved across terminals |
| `NO_SLEEP_DEMO` | `0` stops creating missing demo users; does not delete existing users |
| `NO_SLEEP_SMTP_ADDR`, `NO_SLEEP_SMTP_FROM` | Optional TLS SMTP `host:port` and sender |
| `NO_SLEEP_SMTP_USER`, `NO_SLEEP_SMTP_PASSWORD` | Optional authentication; provide both together |
| `NO_SLEEP_PUBLIC_URL` | HTTPS URL for emailed approved reset links |

Example: `$env:NO_SLEEP_DB = 'data/runtime.db'`, then `go run .`.
SMTP port 465 uses implicit TLS; other ports require STARTTLS. Failed email
delivery preserves the approved code for private admin handoff. Never commit
credentials. Demo mode only permits loopback listening addresses.

### Restore a backup

Stop every server using the database. Substitute the actual backup filename:

```powershell
go run . -restore 'data/backups/your-backup.db'
go run .
```

Restore validates the backup before replacing the saved active database and
retains the previous database/companions under a printed rollback path. Keep
an additional backup outside your computer for protection against disk loss.

## Common problems

| Problem | Fix |
| --- | --- |
| `go` not recognized | Install Go and reopen VS Code |
| Missing `go.mod` | Open/change into the correct folder |
| Port already in use | Ctrl+C in the earlier server terminal; wait for its prompt |
| Cannot connect | Keep Go running; use its URL instead of Live Server |
| Old UI | Save, restart Go and refresh browser |
| Missing data | Check printed database path/local config; do not delete databases |
| Lost action response | Reconnect and inspect ledger/booking before retrying; it may already be saved |
| Time rejected | Check Philippine date, quarter-hour start, 15-minute lead and 30-day limit |
| Database busy | Avoid multiple servers/viewer write transactions; do not delete WAL/SHM files |
| Reset unavailable | Ask admin to verify identity and issue a valid code |

## Developer checks

Go tests use isolated databases. Node is needed only for JavaScript tests.

```powershell
go test ./...
go vet ./...
go build .
node --check web/app.js
node --test tests/*.test.cjs
```

## Suggested smaller student-project scope

**All features are currently kept.** A focused transactional project could keep
accounts/roles, booking, payment/receipt, play/extension, cancellation/refund/history,
PC/rate/maintenance management, reports and admin-approved password recovery.
Optional removals: cashier shifts, SMTP email, rescheduling, Quiet Mode and automatic
backups. A manual backup download can stay. Check your instructor's requirements.

This is a local Go server with writable SQLite, not a ready Vercel deployment.
Shared hosting needs a running backend, persistent storage, HTTPS and proper accounts.
