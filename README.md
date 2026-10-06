# NO SLEEP

NO SLEEP is a gaming café reservation and billing system built with **Go,
SQLite, HTML, CSS, and vanilla JavaScript**. Its dark interface organizes
PC reservations, staff-verified payments, receipts, refunds, and play sessions.

This is the teammate guide: setup in Visual Studio Code, complete user flows,
database and code explanations, sharing instructions, and troubleshooting.
Terminal examples use **Windows PowerShell** unless stated otherwise.

## Contents

- [Quick start](#quick-start)
- [Complete VS Code setup](#complete-vs-code-setup)
- [Sharing and receiving the project](#sharing-and-receiving-the-project)
- [How Go and the browser work together](#how-go-and-the-browser-work-together)
- [Accounts and permissions](#accounts-and-permissions)
- [Booking rules and default rates](#booking-rules-and-default-rates)
- [Complete application workflows](#complete-application-workflows)
- [Database guide](#database-guide)
- [Project structure and code guide](#project-structure-and-code-guide)
- [Backend sequence and transactions](#backend-sequence-and-transactions)
- [API reference](#api-reference)
- [Configuration](#configuration)
- [Team development and checks](#team-development-and-checks)
- [Troubleshooting](#troubleshooting)
- [Deployment and current limits](#deployment-and-current-limits)

## Quick start

Install **Go 1.26 or newer**, extract or clone the project, and open the
`nosleep-github` folder in VS Code. That folder must contain `main.go`
and `go.mod`.

Open **Terminal > New Terminal** and run:

```powershell
go version
go run .
```

Wait for this message:

```text
NO SLEEP is ready at http://127.0.0.1:8080
```

Open **http://127.0.0.1:8080** in your browser and keep the terminal running.
For a first customer test, sign in with `player@nosleep.local` and `Player123!`.
Other demo accounts are listed below.

Press **Ctrl+C** in the server terminal to stop it. Run `go run .` again to
restart. After code edits, save, restart Go, and refresh the browser.

## Complete VS Code setup

### 1. Install the tools

| Tool | Purpose | Required? |
| --- | --- | --- |
| Go 1.26 or newer | Compiles and runs the backend | Yes |
| Visual Studio Code | Editing and integrated terminal | Used in this guide; another editor can also work |
| Modern browser | Displays HTML/CSS and runs JavaScript | Yes |
| Git | Clone, pull, commit, and push | For GitHub collaboration |
| VS Code Go extension | Go editing assistance | Optional |
| Node.js 18 or newer | JavaScript regression tests | Optional; not needed to run the app |
| SQLite viewer | Inspect database tables directly | Optional |

Follow the [official Go installation guide](https://go.dev/doc/install).
On Windows, install Go using the MSI installer, then close and reopen
VS Code so its terminal recognizes the updated environment.

You do **not** need npm, a frontend build step, Live Server, XAMPP, MySQL,
or a separate SQLite server for this project.

### 2. Open the correct folder

1. Extract the ZIP first if you received a ZIP file.
2. Open VS Code and choose **File > Open Folder**.
3. Select the folder containing `main.go`, `go.mod`, `internal`, and `web`.
   If you received the parent folder, select its `nosleep-github` subfolder.
4. Open **Terminal > New Terminal** and select PowerShell if needed.
5. Run `Get-Location` and `Get-ChildItem` to check the terminal's working
   folder. You should see `main.go` and `go.mod` there.

Your teammate can store the project in a different location. Quote paths
with spaces when changing folders, for example:

```powershell
Set-Location 'C:\Projects\NO SLEEP\nosleep-github'
```

Replace that example path with the folder on your own computer.

### 3. Start Go

```powershell
go version
go run .
```

The first run may take longer while Go downloads dependencies from
`go.mod`/`go.sum`. Internet is needed when the dependencies or required
toolchain are not already available locally.

Startup prepares the runtime database, opens/creates tables, applies supported
migrations, seeds missing PCs and tiers, creates missing demo users when
enabled, and starts the web server.

The terminal remaining occupied is normal: the server is waiting for browser
requests. Open another terminal for Git commands while it runs.

### 4. Open the website

Enter **http://127.0.0.1:8080** in the browser address bar. Keep using the same
host address because login cookies belong to that host.

To test customer, cashier, and admin simultaneously, use separate browser
profiles or different browsers. Tabs in one browser profile share login
cookies; signing in as another account changes that browser's session.

### 5. Restart after edits

1. Save the changed files in VS Code.
2. Press **Ctrl+C** in the terminal running Go.
3. Run `go run .` again.
4. Refresh the browser.

The HTML, CSS, and JavaScript are embedded into the Go program when compiled.
Refreshing alone does not load newly edited frontend files into an already
running server. Automatic live reload is not configured.

## Sharing and receiving the project

### Folder or ZIP handoff

Share these files and folders together:

```text
nosleep-github/
  README.md
  .gitignore
  go.mod
  go.sum
  main.go
  internal/app/        Include schema, Go files, tests, and testdata
  web/                 Include index.html, styles.css, and app.js
  tests/               UI checks; useful for team development
  data/nosleep.db      Included starter database
```

Your teammate installs Go, extracts the folder, and follows the VS Code
steps. They do not need your compiler cache or a SQL import. The application
can initialize a database if the starter is missing, but include the starter
when sharing the intended setup.

For an ordinary development handoff, leave local runtime databases, downloaded
backups, `.cache`, `.tools`, build output, logs, and local secrets out of the
**sharing copy**. Keep your own working data. If an earlier version used
`data/nosleep.db` for real records, check its contents before sharing: a new
runtime copies those records too.

**`.gitignore` filters Git additions, not ZIP files.** Zipping the entire
working folder can still include runtime databases and caches. If you
deliberately include `runtime.db`, the receiver starts with its accounts and
records and then changes their own independent copy.

### Git clone handoff

With Git and Go installed:

```powershell
git clone YOUR_GITHUB_REPOSITORY_URL nosleep-github
Set-Location nosleep-github
go run .
```

Replace `YOUR_GITHUB_REPOSITORY_URL` with your actual repository clone URL.
You can also open the cloned folder in VS Code and use its terminal.

Both teammates can use port 8080 on separate computers. Their `127.0.0.1`
addresses refer to their own computers and their databases are separate.
**Git shares code, not live bookings or payments.** Sharing one live dataset
requires using the same running backend with a suitable network/hosting setup.

## How Go and the browser work together

VS Code edits files and provides a terminal. Go runs the backend. The browser
renders HTML/CSS and executes JavaScript.

```text
VS Code terminal: go run .
          |
          v
Go HTTP server on 127.0.0.1:8080
          |
          +-- serves HTML/CSS/JavaScript --> browser interface
          |
          +<-- receives API requests from browser JavaScript
          |
          +-- validates login, permissions, and business rules
          |
          +-- reads/writes SQLite --> returns a browser response
```

The `.` in `go run .` means the current Go package directory. Go compiles and
runs that package; `main()` in `main.go` starts the server. See the official
[Go command reference](https://pkg.go.dev/cmd/go) for command behavior.

`127.0.0.1` means your own computer; `8080` is the listening port. The terminal
prints the address, which you open in the browser. Opening `web/index.html`
directly or through Live Server does not run the Go API/database workflows.

To build and run a Windows executable from the project root:

```powershell
go build -o nosleep.exe .
.\nosleep.exe
```

The executable includes the frontend. Run it from this project folder to use
the same relative `data` directory. Rebuild after source changes.

## Accounts and permissions

### Demo accounts

| Role | Email | Password |
| --- | --- | --- |
| Customer | player@nosleep.local | Player123! |
| Cashier | cashier@nosleep.local | Cashier123! |
| Admin | admin@nosleep.local | Admin123! |

These are local demonstration credentials. Startup creates missing demo users
but does not reset an existing password, role, or active state.

| Role | Functions |
| --- | --- |
| Customer | Register/sign in, check availability, book PCs, view own sessions/receipts, request eligible extensions/refunds, cancel eligible own bookings |
| Cashier | View café bookings and player names, record verified payments, create walk-ins, start/end play, cancel eligible reservations, complete approved refunds, view/export reports |
| Admin | Cashier functions plus PC/rate/user management, maintenance, refund approval/rejection, audit, and database backups |

Registration creates a customer. An admin can change a role in **Operations >
Users**. Deactivation prevents sign-in and invalidates saved login sessions.
The last active admin is protected against demotion/deactivation.

Customers receive their own account bookings. A walk-in's `user_id` records
the staff creator, not a guest account. If that staff member becomes a
customer, they lose guest-booking access while keeping ordinary own account
reservations. Cashiers receive booking customer names without the admin user
directory.

## Booking rules and default rates

- All displayed dates/times use **Philippine time (UTC+8)**.
- Scheduled bookings start at least **15 minutes from server time**.
- Scheduled start minutes are **:00, :15, :30, or :45**.
- Customers can reserve hours or days ahead, up to **30 days** away.
- Duration uses **30-minute blocks**, from 30 minutes to 8 hours (1–16 blocks).
- Staff **Start now** walk-ins use the current server time without the
  scheduled lead or quarter-hour restriction.

At exactly 11:30 PM, the earliest scheduled start is 11:45 PM. At 11:31 PM,
the lead ends at 11:46 PM, so the next allowed start is midnight. Later starts
such as 2:00 AM or another day are also allowed within the 30-day window.

| Tier | Stations | CPU | RAM | GPU | Rate / 30 min | 60-min total |
| --- | --- | --- | --- | --- | --- | --- |
| Standard | PC-01–PC-04 | Intel Core i5-12400F | 16 GB | RTX 4060 | ₱30.00 | ₱60.00 |
| Pro | PC-05–PC-08 | Intel Core i7-12700F | 32 GB | RTX 4070 | ₱45.00 | ₱90.00 |
| Elite | PC-09–PC-12 | Intel Core i9-13900F | 32 GB | RTX 4080 | ₱60.00 | ₱120.00 |

These are demo defaults, not detected hardware. Admins can edit them to match
the café. Custom specs are preserved; original GPU/RAM-only default specs
receive their CPU description on startup.

Regular price = **rate per 30 minutes × number of blocks**. All public amounts use
**Philippine pesos (PHP)**. Enter `30.00` for ₱30.00 or `30.50` for ₱30.50.
The backend calculates exact decimal amounts.

All PCs receive **20% off daily from 1:00 AM until 6:00 AM Philippine time**.
Only billed minutes inside that window receive the discount. A Standard
session from 12:30 AM to 1:30 AM costs ₱54.00: ₱30.00 for the first half-hour
and ₱24.00 for the discounted half-hour. A session starting at 6:00 AM uses
regular pricing. Immediate walk-ins use minute precision for pricing;
seconds still remain accurate in the actual play start. Final charges are
rounded once to two decimal places.

| Tier | Discounted 30 minutes | Discounted 60 minutes |
| --- | --- | --- |
| Standard | ₱24.00 | ₱48.00 |
| Pro | ₱36.00 | ₱72.00 |
| Elite | ₱48.00 | ₱96.00 |

These totals assume the entire session is inside the discount window and
the default tier rates. The preview shows the regular total, night discount,
and final total. Online reservations, scheduled walk-ins, Start now, and
extensions use the same server pricing rule. Each new item stores its price
when held; existing items/payments are preserved. Receipts, refunds, and
reports use the actual stored discounted amounts.

### Availability and holds

Availability covers the entire duration, including a reservation/maintenance
window starting partway through it. Cards distinguish available, reserved,
maintenance, offline, and availability not checked. A failed/loading check
does not prove a station is reserved.

Later-start suggestions are rechecked when chosen; they do not place a hold
by themselves. Manual Refresh and idle rechecks update seats and preserve
drafts. A selected seat is cleared if another person takes it. The server
always checks again when creating the booking.

A scheduled reservation gets a **10-minute payment hold**. Staff must record
verified payment before its deadline. Unpaid expiry releases allocations
when the server processes expiration checks. An extension hold can expire
earlier when the original session ends sooner. Server time determines validity.

## Complete application workflows

### Customer reservation

1. Sign in as customer or create an account.
2. Open **Book a PC** and select date, time, duration, and optional tier.
3. Select an available station and review hardware, dates, rate, and total.
4. Press **Place 10-minute hold** and confirm.
5. Open the booking to see its reference and countdown.
6. Pay café staff before the hold expires.

References look like `NS-000001`. A hold reserves time; it does not mean
payment was received or play has started.

### Cashier payment and receipt

1. Open **Cashier workspace > Payments** as cashier/admin.
2. Identify the customer/guest and held base booking or extension.
3. Choose **Cash** or **External payment**.
4. Verify the actual money outside this website.
5. Review and approve the payment confirmation.

Cash gets an automatic chronological reference such as `CASH-000001`. The
form shows a preview; the server assigns the final number. External payments
require the actual transfer receipt's reference. If already recorded, the
confirmation warns staff to verify a separate received payment before
proceeding. Repeated external references are advisory, not universally blocked.

Payment confirms the base booking and issues a receipt such as `RC-000001`.
Use **Details > Receipts > Print** and the browser's print dialog to print
or save a PDF. The app records collections; it does not transfer money.

### Scheduled check-in and play

1. At the reserved start, open **Cashier workspace > Check-in**.
2. Press **Start play** for the confirmed, paid reservation.
3. Monitor **Playing** or **Today**.
4. Press **End play** when the player actually finishes.

Check-in is valid during the reserved interval and cannot overlap another
actual active play on the PC. Late check-in does not move the booked end.
Paid reservations that pass their end without any play become `no_show`
during expiration processing.

An active session stays active until staff end it, even after its reserved
end. **Start now** blocks that PC until the earlier play is ended. Scheduled
availability describes reserved intervals; staff must also resolve actual
overtime before another check-in. Timers do not switch off physical PCs.

### Walk-in: Schedule for later

1. Open **Cashier workspace > Walk-in > Schedule for later**.
2. Choose date/time, duration, and an available station.
3. Enter the guest name, review, and confirm the 10-minute hold.
4. Record verified payment in **Payments** before expiry.
5. Start play in **Check-in** at the reserved time.

No guest account is created. The guest name belongs to that reservation.
This mode keeps the 15-minute lead and quarter-hour rules.

### Walk-in: Start now

1. Open **Cashier workspace > Walk-in > Start now**.
2. Select duration and a station free for that whole session.
3. Enter guest name and payment method/reference.
4. Verify actual money and review **Amount received**.
5. Confirm **Record payment and start**.

The server saves the booking, payment, receipt, and started play in **one
database transaction** at the actual server time. There is no immediate hold
or 15-minute wait. Future reservations, maintenance, disabled PCs, or ongoing
play can prevent it.

If an admin changes a rate after staff review it, the old quote is rejected
without recording payment. Prices refresh and staff must review again.
Retrying a successful request returns the original session and amount,
rather than charging twice.

### Extension

1. Open **Details > More time** before the confirmed booking ends.
2. Choose additional 30-minute blocks and request an extension.
3. The server checks the added interval and creates an extension payment hold.
4. Staff verify and pay that item through **Payments**.
5. Successful payment moves the booking end; the original charge stays intact.

Extensions use the rate when quoted. Only one extension hold per booking is
allowed at a time. Unpaid expiry releases the added interval without erasing
the original paid booking. Extensions cannot be added after the booked end
or after staff ended play.

### Cancellation and refund

An eligible held/confirmed booking can be cancelled **before its start and
before any play**. Customers cancel own account bookings; staff can cancel
café reservations. Review the confirmation before approving.

- Unpaid cancellation releases the seat without a money refund.
- Paid cancellation releases the seat and creates pending refund requests
  for amounts not already reserved by pending/approved/completed refunds.
- Customers/staff can also request an eligible payment refund with a reason
  from booking details.

```text
Refund request: pending --> admin approves: approved --> money returned: completed
                       \-> admin rejects: rejected
```

Admins approve/reject in **Operations > Refunds**. Cashiers/admins complete
approved refunds in **Cashier workspace > Refunds** only after returning
actual money. Requesting/approving a refund does not itself return money,
cancel a booking, or end play. The server prevents over-refunding a payment.

### Dashboard, History, and Quiet Mode

Customers use **Dashboard**, **My bookings**, and **History** for upcoming,
active, and past sessions. Details contain charges, receipts, refunds, and
extension controls. Each **History** entry also shows its refund status and
amount in Philippine pesos. Pending requests await review; approved requests
await money return; rejected requests are labelled accordingly. **Refunded**
appears only after staff record completion, with the return date and time in
Philippine time. Multiple payment refunds appear separately under the booking.
**Quiet Mode** simplifies the active-session display and
stores its preference in the browser. It does not pause time or change billing.

### Cashier Today view

**Today** groups pending payments, arriving players, active sessions, and
sessions with 15 minutes or less remaining. Overnight sessions remain visible.
Check-in controls and badges update at time boundaries. Background updates
respect focused forms/open confirmations; use **Refresh** for an explicit check.

### Admin management

| Operations section | Function |
| --- | --- |
| Overview | Collection totals, active counts, recent payments, backup download |
| Reports | Date-filtered collections/completed refunds and CSV |
| PC inventory | Station code, tier, CPU/RAM/GPU description, enabled state |
| Rates | Tier names and peso rate per 30 minutes |
| Users | Role and account activation |
| Maintenance | PC, interval, private notes, optional public explanation |
| Refunds | Approval/rejection decisions |
| Audit | Recorded actions, actor IDs, timestamps |

Maintenance rejects conflicts with allocated sessions. Private staff notes
remain staff-only; the public explanation can describe downtime on customer
cards. Legacy maintenance reasons stay private. Disabling a PC is restricted
when future allocated time exists, so resolve affected bookings first.

### Reports and CSV export

1. Open **Reports** as cashier/admin.
2. Choose **From** and **To**, then **Apply dates**, or use a date shortcut.
3. Check the displayed range/totals and press **Download CSV**.

Dates are inclusive Philippine calendar days. Payments count when recorded;
refunds count when their completion is recorded. Pending/approved refunds
are not completed-refund deductions. Net = payments minus completed refunds
in that range. A day may have negative net if older payments are refunded.

CSV contains payment/refund rows, booking references, customer/guest names,
decimal peso amounts, and totals. Formula-like user text is escaped. Export
is disabled for loading/failed reports or an edited range not yet applied.

### Backup and restore

Admin: open **Operations > Overview > Download database backup**. It downloads
a consistent SQLite snapshot, including committed write-ahead-log records if
WAL is in use. It contains accounts, authentication records, guest names,
bookings, payments, and the other tables; keep it private. CSV is a report,
not a complete backup.

To restore locally:

1. Stop Go with **Ctrl+C**.
2. Preserve your existing working database.
3. Copy the downloaded backup to `data`, for example `restored.sqlite`.
4. In PowerShell, run:

```powershell
$env:NO_SLEEP_DB = 'data/restored.sqlite'
go run .
```

This opens the restored file directly. Set that variable again in a new
terminal to continue using it. Configuration below explains clearing overrides.

## Database guide

### Which database is used?

The database is **SQLite**, using the `modernc.org/sqlite` Go driver. It is a
local file; no database service or database login credentials are needed.

| File | Purpose |
| --- | --- |
| `data/nosleep.db` | Included starter, read-only source for the first default runtime copy |
| `data/runtime.db` | Default working file; saves local accounts/bookings/payments/sessions |
| Path in `NO_SLEEP_DB` | Custom working file for that run |

An existing runtime is kept. If missing, startup snapshots the starter into
it. If the starter is missing too, startup initializes a new database.
Supported older schemas receive migrations without discarding their records.
Previously used starters also copy their existing records into a new runtime.

Relative paths resolve from the server's working directory. Running from
another folder can select another `data` folder. An explicit `NO_SLEEP_DB`
opens/creates that file directly; it does not copy the starter to a new custom
path automatically.

### Where to see data

Use booking details, cashier screens, reports, and admin screens for normal
operations. For direct inspection, open the **active working database** in a
SQLite viewer: normally `data/runtime.db`, or the configured custom file.
Opening the starter normally shows no new runtime activity.

Inspect read-only or open an admin backup. Direct SQL edits bypass application
rules/audit. Example read-only queries:

```sql
SELECT id, reference, user_id, pc_id, status, start_at, end_at
FROM bookings ORDER BY id DESC;

SELECT id, booking_id, method, reference, amount_pesos, created_at
FROM payments ORDER BY id DESC;

SELECT booking_id, guest_name FROM walk_ins;

SELECT id, code, tier_id, specs, enabled FROM pcs;
```

Time columns are Unix seconds in UTC; the browser displays Philippine time.
To view a stored start in Philippine time in SQLite:

```sql
SELECT reference, datetime(start_at, 'unixepoch', '+8 hours') AS start_ph
FROM bookings;
```

### Tables and relationships

Defined in `internal/app/schema.sql`:

| Table | Data |
| --- | --- |
| `users` | Names/emails, password hashes, roles, activation |
| `auth_sessions` | Hashed login tokens, user links, expiry |
| `tiers` | Names and exact peso rates |
| `pcs` | Codes, tiers, specs, enabled flags |
| `maintenance` | PC intervals, private `reason`, public `public_reason` |
| `bookings` | Reference, creator/owner, PC, interval, status, hold deadline |
| `walk_ins` | Guest name linked to a booking |
| `booking_items` | Base/extension quotes, rates/totals, payment-hold status |
| `booking_slots` | Allocated 30-minute intervals for overlap checks |
| `payments` | Verified amounts, method/reference, cashier, timestamp |
| `receipts` | Receipt numbers and payment/booking links |
| `plays` | Actual start and optional actual end |
| `refunds` | Payment-linked amounts/reasons, decisions, completion |
| `audit` | Actor, action, affected record, time |

One booking has one PC, a base charge, and optional extension items. Each paid
item has a payment/receipt. Walk-ins add guest metadata rather than guest
accounts. A booking can have one play record. Refunds link to payment/booking.

Price columns store exact decimal text such as `30.00`; Go's `Pesos` type
performs exact arithmetic. Inputs, JSON fields, stored price columns, and UI
labels describe Philippine pesos. Internal representation is a code detail.

### Booking status versus play state

| Booking status | Meaning |
| --- | --- |
| `held` | Payment deadline running |
| `confirmed` | Base paid; waiting for check-in or already playing |
| `completed` | Staff ended play |
| `cancelled` | Eligible reservation cancelled |
| `expired` | Unpaid base hold expired |
| `no_show` | Paid interval ended without any play record |

There is no `active` booking status in the schema. Active play means a `plays`
row has `ended_at` unset while the booking stays `confirmed`. Item and refund
statuses are separate from booking statuses.

## Project structure and code guide

```text
nosleep-github/
├── main.go
├── go.mod / go.sum
├── README.md / .gitignore
├── web/
│   ├── index.html
│   ├── styles.css
│   └── app.js
├── internal/app/
│   ├── http.go / auth_limits.go
│   ├── models.go / store.go / schema.sql / snapshot.go
│   ├── bookings.go / walkin.go / admin.go
│   ├── operations.go / tools.go / runtime.go
│   ├── pesos.go / peso_migration.go
│   ├── *_test.go
│   └── testdata/legacy.sql
├── tests/
│   ├── experience.test.cjs
│   └── payment-confirmation.test.cjs
└── data/
    ├── nosleep.db
    └── runtime.db       Created locally; ignored by Git
```

| File | Responsibility |
| --- | --- |
| `main.go` | Configuration, embedded frontend, runtime preparation, server start/shutdown |
| `web/index.html` | Page shell, app container, confirmation dialog, print area |
| `web/styles.css` | Dark/mint styling, responsive layout, dialogs, print styles |
| `web/app.js` | Views, forms, API calls, adjusted clocks, polling, confirmations, print, Quiet Mode |
| `internal/app/http.go` | Routes, JSON validation, login/cookies, CSRF/origin checks, responses/errors, assets |
| `internal/app/auth_limits.go` | Failed/in-flight account/IP login and registration limits |
| `internal/app/models.go` | Go structures and JSON field names |
| `internal/app/store.go` | SQLite initialization/seeding, permissions, transactions, scans, expiry |
| `internal/app/schema.sql` | Tables, relationships, checks, uniqueness |
| `internal/app/snapshot.go` | Role-filtered state, customer names, maintenance privacy |
| `internal/app/bookings.go` | Holds, allocations, payments/receipts, cancellation, play, extensions |
| `internal/app/walkin.go` | Guests, scheduled walk-ins, immediate availability and quoted Start now |
| `internal/app/admin.go` | PC/tier edits, user access, maintenance, refund operations |
| `internal/app/operations.go` | Availability reasons/alternatives and Philippine-date reports |
| `internal/app/tools.go` | Reference lookup, CSV, admin backup downloads |
| `internal/app/runtime.go` | Safe starter copy and consistent SQLite snapshots |
| `internal/app/pesos.go` | Exact parsing/formatting, JSON/SQL amounts, refund calculations |
| `internal/app/peso_migration.go` | Supported earlier money-schema conversion without double conversion |
| Go `*_test.go` files | Backend/database/HTTP regression checks |
| `tests/*.test.cjs` | JavaScript behavior and confirmation checks |

### go.mod and go.sum

`go.mod` identifies the module as `nosleep`, declares its Go version, and pins
dependency versions. `go.sum` records module checksums used to verify downloaded
dependencies. Keep both in Git and shared folders.

The direct dependencies are `modernc.org/sqlite` and `golang.org/x/crypto`
(bcrypt password hashing). Entries marked `indirect` support those libraries.
Go downloads them; your teammate does not need your local dependency cache.

### Key backend functions

| Function | What it does |
| --- | --- |
| `main()` | Prepares configuration, database, assets, and the running server |
| `OpenStore()` / `Seed()` | Open/initialize SQLite and add missing defaults |
| `NewHandler()` | Build the HTTP routes and seed missing demo users when enabled |
| `Snapshot()` | Assemble the current user's permitted data for the UI |
| `CreateBooking()` / `CreateWalkIn()` | Validate and save a scheduled reservation hold |
| `AvailabilityDetails()` / `WalkInAvailability()` | Check scheduled/immediate intervals and explain blocked PCs |
| `RecordPayment()` | Record a verified held item's payment and receipt, confirming or extending the booking |
| `StartWalkInNowQuoted()` | Verify the reviewed peso amount, then create/pay/start a guest booking atomically |
| `StartPlay()` / `EndPlay()` | Record actual check-in/end with staff permissions |
| `ExtendBooking()` | Create an extra-time quote and payment hold |
| `CancelBooking()` | Release an eligible reservation and create owed refund requests |
| `RequestRefund()` / `DecideRefund()` | Request, approve/reject, or complete a payment-linked refund |
| `SetMaintenanceDetails()` | Schedule a blocked interval with separate private/public explanations |
| `ReportForDates()` | Filter collections/completed refunds by Philippine dates |
| `PaymentReferences()` / `DatabaseBackup()` | Staff duplicate-reference lookup and admin database snapshot |
| `PrepareRuntimeDatabase()` | Create the initial runtime copy without overwriting an existing runtime |

### Reading the Go code

`package main` identifies the runnable entry package. Files in
`internal/app` use `package app` and work together as the backend package.
`import` brings in libraries; `func` declares a function; `type ... struct`
defines related fields, such as a `Booking` or `Payment`.

A declaration such as `func (s *Store) CreateBooking(...)` is a method on
the database store. An error return is checked with `if err != nil`; a
failed operation returns an error rather than continuing as a success.
JSON tags in `models.go`, such as `json:"amount_pesos"`, connect Go fields
to the names JavaScript receives. SQL parameters such as `?` bind supplied
values instead of building SQL from user text. Files ending in `_test.go`
are used by `go test`, rather than compiled into the normal running app.

## Backend sequence and transactions

### Startup

```text
go run .
  -> main(): read configuration
  -> prepare default runtime if missing
  -> OpenStore(): open SQLite, ensure schema, apply migrations
  -> NewHandler(): seed missing defaults/demo users, register routes
  -> ListenAndServe(): receive browser requests
  -> browser loads embedded HTML/CSS/JS
  -> app.js requests /api/me, then authorized /api/state
```

### Scheduled booking to play

```text
Customer selects date/duration -> GET /api/availability
Customer confirms hold        -> POST /api/bookings
  Server commits held booking + charge + allocated slots + audit
Cashier verifies actual money -> POST /api/payments
  Server commits payment + receipt + paid item + confirmed booking + audit
Staff checks player in        -> POST /api/play/start
Staff ends actual play        -> POST /api/play/end
```

JavaScript shows previews; Go validates permissions, eligibility, deadlines,
amounts, and overlaps. A SQL transaction commits related writes together or
rolls them all back if a step fails. Start now, for example, must not keep a
payment if creating its play record fails. Retry keys prevent repeating
supported checkout/payment/extension operations; constraints also prevent
duplicate payments per item.

The local store serializes SQLite transactions through one connection. Time
interval checks catch partial overlaps despite 30-minute allocation blocks.
Passwords use bcrypt, login tokens are random and stored hashed, and POSTs
validate form-session tokens/origins. Permissions use the database role,
rather than trusting browser-supplied roles. Login failure/in-flight limits
are separate from registration; success does not consume failure quotas.

Timers update each second and state normally polls every five seconds when
appropriate. Refresh/focus/visibility and idle checks help keep seats current.
This is polling, not WebSockets or an automatic payment processor.

## API reference

The UI already sends these requests. This table helps developers find code;
manual API calls are not necessary to operate the application.

| Method | Route | Purpose / permission |
| --- | --- | --- |
| GET | `/health` | Health; no login |
| GET | `/api/me` | Current user and form-session token |
| POST | `/api/register` | Create customer and sign in |
| POST | `/api/login` | Sign in |
| POST | `/api/logout` | Sign out |
| GET | `/api/state` | Role-filtered snapshot |
| GET | `/api/availability?start=...&slots=...` | Scheduled availability; signed-in users |
| POST | `/api/bookings` | Scheduled hold |
| POST | `/api/bookings/cancel` | Eligible own/staff cancellation |
| POST | `/api/payments` | Staff verified collection/receipt |
| POST | `/api/play/start` | Staff check-in |
| POST | `/api/play/end` | Staff end play |
| POST | `/api/extensions` | Eligible extension hold |
| POST | `/api/walk-ins` | Staff scheduled guest hold |
| GET | `/api/walk-ins/availability?slots=...` | Staff immediate duration check |
| POST | `/api/walk-ins/start-now` | Staff quoted atomic payment/start |
| POST | `/api/refunds` | Eligible refund request |
| POST | `/api/refunds/action` | Admin approve/reject; staff complete |
| GET | `/api/reports?from=...&to=...` | Staff date report |
| GET | `/api/reports.csv?from=...&to=...` | Staff CSV download |
| GET | `/api/payment-reference?reference=...` | Staff duplicate external-reference advisory |
| GET | `/api/admin/backup` | Admin SQLite download |
| POST | `/api/admin/pcs` | Admin saves PC |
| POST | `/api/admin/tiers` | Admin saves tier/rate |
| POST | `/api/admin/maintenance` | Admin maintenance window |
| POST | `/api/admin/users/role` | Admin role change |
| POST | `/api/admin/users/active` | Admin activation change |

POSTs use JSON, the current form-session cookie plus matching `X-CSRF-Token`,
and a login cookie where authenticated. Unknown JSON fields are rejected.
Report dates use `YYYY-MM-DD`; booking timestamps use RFC 3339 with timezone.
`slots` means duration blocks, not the 15-minute spacing between start times.

Example Start now body sent by the staff form:

```json
{
  "pc_id": 1,
  "slots": 2,
  "guest_name": "Sample Guest",
  "method": "cash",
  "reference": "",
  "key": "unique-request-key",
  "expected_amount_pesos": "60.00"
}
```

It still requires authentication, form tokens, eligibility, and an exact
matching quote. Typical statuses: 400 invalid input, 401 authentication,
403 permissions/form checks, 404 missing record, 409 conflict, 429 attempt limit.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `NO_SLEEP_ADDR` | `127.0.0.1:8080` | Listening address/port |
| `NO_SLEEP_DB` | `data/runtime.db` | Working SQLite file |
| `NO_SLEEP_DEMO` | Enabled unless exactly `0` | Create missing demo users |

Set variables in the **same terminal** before starting. They are not loaded
automatically from a `.env` file.

For the usual local workflow, keep the default port 8080. Run `go run .`
in the VS Code terminal and press **Ctrl+C in that same terminal** to stop
the program. Wait for the PowerShell prompt before starting it again.
If you previously set another port, clear that override in the same terminal:

```powershell
Remove-Item Env:\NO_SLEEP_ADDR -ErrorAction SilentlyContinue
go run .
```

Then open http://127.0.0.1:8080. Keep one server running for this project.

A custom database:

```powershell
$env:NO_SLEEP_DB = 'data/local.db'
go run .
```

Existing data stays; a missing custom file is initialized with missing defaults.
Use a known backup path to restore known data.

Disable missing-demo-user seeding:

```powershell
$env:NO_SLEEP_DEMO = '0'
go run .
```

This does not remove existing demo accounts from a starter/runtime/backup.
A new database with demo disabled has no automatic admin; plan admin
provisioning before using it for a real deployment.

Clear overrides after stopping the server:

```powershell
Remove-Item Env:\NO_SLEEP_ADDR -ErrorAction SilentlyContinue
Remove-Item Env:\NO_SLEEP_DB -ErrorAction SilentlyContinue
Remove-Item Env:\NO_SLEEP_DEMO -ErrorAction SilentlyContinue
go run .
```

These remove terminal environment settings, not database files. New terminals
need custom settings set again unless configured separately. On macOS/Linux,
use your shell's syntax, such as `unset NO_SLEEP_ADDR`, rather than
PowerShell syntax.

## Team development and checks

### Editing responsibilities

Work inside **nosleep-github**. Edit frontend in `web`, backend rules/APIs in
`internal/app`, and startup in `main.go`. Keep migrations compatible with an
existing teammate database: editing `schema.sql` alone does not add columns
to tables that already exist. Agree on JSON fields when splitting Go and JS
work, such as the reviewed `expected_amount_pesos` field.

### What .gitignore does

It excludes runtime databases/SQLite companion files, caches, executables,
logs, and secrets from ordinary Git additions. The starter `data/nosleep.db`
is explicitly included. It does not delete files, prevent runtime use,
untrack an already-committed file, or filter ZIP contents.

### Git workflow

For an already-connected team repository:

1. Stop Go before pulling backend/database changes.
2. Check `git status` and preserve uncommitted work before pulling.
3. Pull with `git pull`.
4. Edit, save, and run checks.
5. Review `git diff`, stage intended source, commit, and push to the agreed branch.
6. Restart Go and refresh.

Example README-only commit:

```powershell
git status
git diff
git add README.md
git commit -m "Document teammate setup and workflows"
git push
```

Stage the right files for your actual change. Do not force-add a working
database to share new code. A ZIP without `.git` still runs, but pull/commit/
push require cloning or setting up a repository first.

### Verification commands

From the project root:

```powershell
go test ./...
go vet ./...
```

Backend tests cover money/migrations, ownership, time boundaries/overlaps,
cash references, payment/refund rules, immediate sessions/retries, reports,
login limits, and database copies/backups. They use disposable databases.

Optional UI checks with Node.js 18 or newer:

```powershell
node --check web/app.js
node --test tests/*.test.cjs
```

These cover confirmations, identity/cancellation guards, timers, drafts/focus,
availability, reports, and reviewed amounts. They simulate the DOM/network;
also inspect actual browser screens when changing CSS/layout/browser behavior.

### Suggested teammate demo

1. Start Go and use separate customer and cashier/admin browser profiles.
2. Hold a Standard PC for a valid future 60-minute session: default ₱60.00.
3. Record verified demo cash payment; check reference and receipt.
4. Export today's Philippine-date CSV.
5. Cancel before start, approve the pending refund as admin, and complete
   it as staff after the simulated money return.
6. Try **Walk-in > Start now** on a free PC; inspect receipt and active play.
7. End play, schedule nonconflicting maintenance, and view its public explanation.
8. Download an admin backup and inspect it in a SQLite viewer.

Use local demo data. Staff verification represents the real-world cashier's
action; the app itself does not collect or send funds.

## Troubleshooting

| Problem | Check or solution |
| --- | --- |
| `go` not recognized | Install Go, reopen VS Code, verify `go version` and PATH |
| Old Go version | Install Go 1.26 or newer as declared in `go.mod` |
| Missing `go.mod` / no Go files | Change into the folder containing `go.mod` and `main.go` |
| Downloads fail | Check internet/proxy access; try `go mod download` from the project root |
| Terminal stays busy | Normal server behavior; keep it running and open the browser |
| Browser cannot connect | Check terminal logs, running server, exact address/port |
| Port already in use | Press Ctrl+C in the terminal running the earlier server, wait for its prompt, then run `go run .` again |
| HTML displays but API fails | Use the Go server URL instead of a file URL/Live Server |
| UI edits do not appear | Save, restart/rebuild Go, refresh browser |
| Database has no new records | Inspect runtime/custom file, not the starter |
| Data appears missing after moving | Check working directory and `NO_SLEEP_DB`; another file may be open |
| Database locked/busy | Close viewer write transactions; avoid two servers on one DB; do not delete SQLite companion files to bypass locks |
| No admin with demo disabled | New databases do not get an automatic admin; check intended existing/demo setup |
| Demo credentials fail | Existing password/role/activation are preserved; check active DB/account state |
| Time rejected | Check Philippine date, 15-minute lead, quarter-hour start, 30-day limit; midnight is the next date |
| PC unavailable | Check whole duration, maintenance, enabled state; resolve active play for Start now/check-in |
| Availability check fails | Keep server running and retry Refresh; failure is not confirmed occupancy |
| Hold expired | Make a new valid hold; expired payment cannot be recorded |
| Start now rate changed | Review refreshed price and confirm again; rejected quote saves no collection |
| Refund not deducted yet | Only completion after money return counts; approval is not completion |
| Report day seems wrong | Payment recording/refund completion use Philippine dates, not booking dates |
| Too many attempts | Wait for the 15-minute attempt window instead of repeated retries |
| Form/permission error | Refresh/sign in as needed; keep same host and check role |

Do not delete your working database to fix a setup problem. Identify its
path and preserve a backup before intentional data changes.

## Deployment and current limits

This repository is configured for **local development**: a standalone Go HTTP
server and writable SQLite file. It has no Vercel/serverless deployment
configuration. Uploading frontend files alone does not run Go transactions
or provide the database.

A shared deployment needs a running backend, persistent storage, HTTPS, real
account provisioning, and backups. Adapting to a serverless environment
requires reviewing the server entry point and storage. Demo mode refuses a
non-loopback listening address. Disabling seeding does not remove already
stored demo credentials; account provisioning must also be handled.

Current boundaries:

- No bank, wallet, payment-gateway, or automatic refund integration.
- No physical-PC power control or automatic game termination.
- Git does not synchronize live data between local clones.
- No password-reset/email-verification service or automatic notifications.
- Backups are manual downloads; no automatic backup schedule.
- SQLite writes are serialized for this local app; larger deployments should
  evaluate concurrency and storage requirements.

For a teammate's local setup, install Go, open this project, run `go run .`,
and open the printed browser address. Source and the starter are portable;
local compiler caches are not needed for the handoff.
#   n o s l e e p - g i t h u b  
 