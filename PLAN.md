# Full Plan: What We Have Done

This document summarizes all features, flows, and fixes implemented for the Taxi MVP (Telegram rider + driver bots, Turso backend, Mini App for drivers).

---

## 1. Health Check & Monitoring

- **Problem:** External monitor (e.g. Render) was getting **404** on `GET /health` or `HEAD /health`.
- **Fix:**
  - Registered both `GET /health` and `HEAD /health` with the same handler (monitors often use HEAD).
  - Added `GET /` that returns the same OK response so root URL checks pass.
- **Files:** `internal/server/server.go`

---

## 2. Driver Application (No Approval)

- **Goal:** Drivers must submit application data (phone, car type, color, plate) once; no admin approval — data is for the client to see who is coming.
- **Implementation:**
  - **DB:** New migration `003_driver_application.sql` adds to `drivers`: `phone`, `car_type`, `color`, `plate`, `application_step`.
  - **Flow:** On driver `/start`, if any of these are missing, the bot asks in order: phone → car type → color → plate. After plate, driver is asked for location.
  - **Phone step:** Reply keyboard with **“📞 Telefon raqamini yuborish”** (share contact) so driver can share number in one tap; typing the number is also accepted.
  - **Persistence:** All four fields are saved in `drivers`; on next `/start` they are not asked again.
  - **Going online:** Driver can go online only after application is complete and location has been shared at least once.
- **Files:** `db/migrations/003_driver_application.sql`, `internal/bot/driver/bot.go` (application prompts, `handleApplicationText`, contact handling, `inferApplicationStep`)

---

## 3. Driver Bot: Application Flow Robustness

- **Problem:** After sharing contact, the bot sometimes did not move to the next step (e.g. car type).
- **Fixes:**
  - **Infer next step from DB:** If `application_step` column is missing or not set, infer the next step from which of phone/car_type/color/plate is still empty, so the flow works even before/without migration.
  - **Always advance UI:** After saving each field, always send the next question (or “Ilova qabul qilindi”) even if the DB write fails, so the user is never stuck.
  - **Final step (plate):** After saving plate, send one message with both the “Ilova qabul qilindi ✅ Endi lokatsiyangizni yuboring.” text and the **location keyboard** (not an empty message + keyboard), so the “Lokatsiya yuborish” button replaces the phone button correctly.
- **Files:** `internal/bot/driver/bot.go` (`inferApplicationStep`, `setApplicationStep`, `clearApplicationStep`, `handleApplicationText`, `sendApplicationPrompt`)

---

## 4. Rider: Phone Required Before Order

- **Goal:** Client (rider) must share phone number before creating an order; store it once so they are not asked every time.
- **Implementation:**
  - On rider `/start` or before handling location: if `users.phone` is empty, show **“📞 Telefon raqamini yuborish”** (share contact) and do not allow creating a request until phone is saved.
  - When rider sends contact, save to `users.phone` and then show the location prompt.
  - On “search again” (after cancel/expiry) and after cancel: again require phone if missing, then show location.
  - Creating a ride request (sending location) is allowed only when phone is present.
- **Files:** `internal/bot/rider/bot.go` (`ensureRiderPhone`, `handlePhoneContact`, `sendLocationPrompt`; `handleStart`, `handleLocation`, `handleCancel`, `handleCallback`)

---

## 5. Notify Rider with Driver Details When Assigned

- **Goal:** When a driver accepts, the rider sees who is coming: driver phone, car type, color, plate.
- **Implementation:** In `AssignmentService.TryAssign`, after assigning the request and creating the trip, load driver’s `phone`, `car_type`, `color`, `plate` from `drivers` and append to the “Haydovchi topildi ✅” message sent to the rider.
- **Files:** `internal/services/assignment_service.go`

---

## 6. Driver Mini App: Show Client Phone (and Name)

- **Goal:** On the driver’s map (Mini App), show the client’s phone number (and name) so the driver can call.
- **Implementation:**
  - **Backend:** `GET /trip/:id` response extended with `rider_phone`, `rider_name`, and `rider_info: { phone, name }` (from `users` for the trip’s `rider_user_id`).
  - **Frontend:** Mini App already expected `rider_phone` / `rider_info.phone` and displays them; no frontend change required once backend is deployed.
- **Files:** `internal/handlers/trip.go` (TripInfoResponse, TripInfo handler)

---

## 7. Orders Only to Eligible Drivers; Not to Driver on a Trip

- **Goal:** Push new orders only to drivers who are free (no active trip). Drivers who have accepted or started a trip must not receive new order notifications until that trip is finished.
- **Implementation:**
  - In `MatchService.BroadcastRequest`, restrict eligible drivers with:  
    `NOT EXISTS (SELECT 1 FROM trips t WHERE t.driver_user_id = d.user_id AND t.status IN ('WAITING','STARTED'))`.  
    So only drivers with no WAITING/STARTED trip get new requests (drivers who finished a trip are eligible again).
  - Kept requirement: driver must have application filled (or use fallback query if migration not run), `is_active = 1`, recent `last_seen_at` (or NULL), and location set.
- **Files:** `internal/services/match_service.go`

---

## 8. After Trip Finish: Location First, Then “Available”

- **Goal:** When a trip is finished, first ask the driver to send their current location; only after location is received, set them as available in the backend.
- **Implementation:**
  - **On trip finish:** Send fare summary to driver, set `drivers.is_active = 0`, then send a single prompt: “Yangi buyurtmalar olish uchun avval joriy lokatsiyangizni yuboring.” with only the **“Joriy lokatsiyani yuborish”** button (no Online/Offline keyboard yet).
  - **When driver sends location:** In driver bot `handleLocation`, if driver is offline (`is_active = 0`) and has no WAITING/STARTED trip, update `last_lat`, `last_lng`, `last_seen_at` and set `is_active = 1` (available). Then send “Lokatsiya qabul qilindi. Siz endi mavjudsiz — yangi buyurtmalar keladi.” and show the usual Online/Offline/Lokatsiyani yangilash keyboard.
- **Files:** `internal/services/trip_service.go` (FinishTrip), `internal/bot/driver/bot.go` (handleLocation)
- **Current behaviour (see also §9, §32):** Trip finish sends a **single** driver message: "Safar tugadi ✅", Masofa, Narx, with the 4-button keyboard. No duplicate "Lokatsiya yuborilsa" message. When the driver sends location (bot or Mini App), they become available and get pending orders; no need to press Online again. Live-location reminder after trip only when driver is not sharing live (with delay and 15s skip if location just updated).

---

## 9. Pushing Orders to Drivers Reliably

- **Goal:** Ensure new and pending orders are pushed to drivers who have shared location; sharing location = online (no need to press Online again).
- **Fixes:**
  - **Sharing location = online:** Bot (static and live location) and Mini App (`POST /driver/location`) set `is_active = 1` and `manual_offline = 0` when the driver has no active trip, then run `NotifyDriverOfPendingRequests` so **pending** requests in radius are pushed. New requests are pushed via `BroadcastRequest` → priority dispatch to drivers with fresh location.
  - **Location freshness (dispatch accuracy):** Only drivers with **last_seen_at within 90 seconds** are eligible for dispatch (initial broadcast and pending redispatch). Stale locations are excluded; constant `driverLocationFreshnessSeconds = 90`.
  - **Only PENDING requests:** Load request `status` and skip broadcasting if not `PENDING`.
  - **Fallback query:** If the main query fails (e.g. migration 003 not run), run a fallback query that only requires `is_active`, location, and no active trip.
  - **NotifyDriverOfPendingRequests:** After an 800 ms delay (so a simultaneous rider request can commit), sends any PENDING requests within the driver’s radius that have not already been sent to that driver; skips if driver’s location is older than 90 s.
- **Files:** `internal/services/match_service.go`, `internal/bot/driver/bot.go`, `internal/handlers/driver_location.go`

---

## 10. Driver with WAITING Trip Sends Location

- **Goal:** If a driver has accepted an order (trip WAITING) and sends location from the bot, guide them to start the trip instead of a generic “trip not started” message.
- **Implementation:** When driver has a WAITING trip and sends location, fetch that trip’s `id`, then send a clear message and **“Open Trip Map”** button again so they can open the Mini App and press “SAFARNI BOSHLASH”.
- **Files:** `internal/bot/driver/bot.go` (handleLocation: waitingTripID, sendWithOpenTripMapButton)

---

## 11. Match Service: Only Drivers with Application Filled

- **Goal:** Only drivers who completed the application (phone, car type, color, plate) should receive order notifications.
- **Implementation:** Main broadcast query includes conditions on `drivers.phone`, `car_type`, `color`, `plate` (all non-empty). If that query fails (e.g. columns not present), fallback query runs without these fields so orders still get pushed.
- **Files:** `internal/services/match_service.go`

---

## 12. Deployment & Troubleshooting (Render)

- **Migrations:** README and Render section state that migrations **must** be run against the same Turso DB used in production (same `TURSO_DATABASE_URL` and `TURSO_AUTH_TOKEN`). Otherwise the app hits “no such table: ride_requests”.
- **Single instance:** Telegram allows only one `getUpdates` connection per bot. README and troubleshooting table instruct: on Render set **Instance count** to **1** to avoid “Conflict: terminated by other getUpdates request”.
- **Troubleshooting table:** Added for `no such table: ride_requests` and getUpdates conflict with concrete fix steps.
- **Files:** `README.md` (Render Option B, Troubleshooting section)

---

## 13. Database Migrations (Summary)

| Migration | Purpose |
|-----------|---------|
| `001_init.sql` | `users`, `drivers`, `ride_requests`, `request_notifications`, `trips`, `trip_locations`; indexes. |
| `002_add_drop_and_radius_expansion.sql` | `ride_requests`: `drop_lat`, `drop_lng`, `radius_expanded_at`. |
| `003_driver_application.sql` | `drivers`: `phone`, `car_type`, `color`, `plate`, `application_step` for driver application (no approval). |
| `004_dispatch_and_cancel.sql` | `request_notifications` with `id`, `status`; trips status CHECK includes `CANCELLED_BY_DRIVER`, `CANCELLED_BY_RIDER`; `trips.distance_m`, `trips.fare_amount`. |
| `005_cancellation_metadata.sql` | `trips`: `cancelled_at`, `cancelled_by`, `cancel_reason`. |
| `006_fare_settings.sql` | Fare settings (tiered tariffs, commission %). |
| `007_drivers_balance_and_payments.sql` | `drivers`: `balance`, `total_paid`; `payments` table. |
| `008_grid_index.sql` | Grid index for dispatch. |
| `009_fare_settings_commission.sql` | Fare settings commission percent. |
| `010_driver_manual_offline.sql` | `drivers`: `manual_offline` (location share can auto-reactivate unless manual offline). |
| `011_driver_live_location_hint.sql` | `drivers`: `last_live_location_at`, `live_location_hint_last_sent_at` for Live Location UX. |
| `012_driver_on_online_hint_sent.sql` | `drivers`: `live_location_on_online_hint_last_sent_at` (separate cooldown for on-Online hint). |
| `013_driver_live_location_active.sql` | `drivers`: `live_location_active` for Live Location–only dispatch. |
| `014_driver_offline_live_reminder.sql` | `drivers`: `live_location_offline_reminder_last_sent_at`. |
| `015_driver_static_rejection_cooldown.sql` | `drivers`: `static_location_rejection_last_sent_at`. |
| `016_payments_trip_id.sql` | `payments`: `trip_id` (nullable) to link commission to trip for `total_price` in admin API. |

---

## 14. API & Backend Summary

- **Health:** `GET /health`, `HEAD /health`, `GET /` → 200 OK.
- **Trip info:** `GET /trip/:id` → trip + pickup/drop/driver position + `driver_info`, `rider_phone`/`rider_name`/`rider_info`. Always returns **live** `distance_km` and `fare` (from current `trips.distance_m` during STARTED; final stored **normalized** values after FINISHED). Response includes nested **`trip`** object: `id`, `status`, `distance_m`, `distance_km`, `fare`, `fare_amount` (null until FINISHED).
- **Driver location:** `POST /driver/location` (auth via initData or `X-Driver-Id`); body: lat, lng, optional accuracy. Returns `{"ok": true}` or `{"ok": true, "ignored": "reason"}`.
- **Trip lifecycle:** `POST /trip/start`, `POST /trip/finish`, `POST /trip/cancel/driver`, `POST /trip/cancel/rider` (driver/rider auth). Responses: `{"ok": true, "trip_id", "status", "result": "updated"}` or `{"ok": true, "result": "noop"}`.
- **Admin payments:** `GET /admin/payments` and `GET /admin/payments?driver_id=<id>` return payment list (id, driver_id, amount, type, note, created_at) plus **total_price** (number) when the payment is linked to a trip (e.g. commission); omitted/null for deposits and adjustments.
- **Order broadcast:** When a rider creates a request, `MatchService` runs priority dispatch; eligible drivers (online, live location active and fresh, application filled, no WAITING/STARTED trip, in radius, balance > 0 unless InfiniteDriverBalance) receive “Yangi so'rov … Qabul qilasizmi?” with Accept button.

---

## 15. End-to-End Flows (Summary)

1. **Rider:** Start → share phone (if needed) → share location → request created and broadcast to drivers → wait or cancel.
2. **Driver:** Start → complete application (phone, car type, color, plate) → share location → becomes online → receives order notifications → Accept → open Mini App → start trip → finish trip → asked to send location → after location, set available and shown Online/Offline keyboard.
3. **After driver accepts:** Rider sees “Haydovchi topildi” + driver phone, car, color, plate; driver sees “Open Trip Map” and can open Mini App to see client phone/name and run the trip.

---

## 16. Smart Driver Dispatch (Priority, One-by-One)

- **Goal:** Replace “broadcast to all drivers at once” with a **priority queue**:
  1. Find eligible drivers within radius.
  2. Sort by distance to pickup (closest first).
  3. Notify the closest driver.
  4. Wait **8 seconds**.
  5. If still not accepted (request still PENDING) → notify the next driver.
- **Implementation:**
  - `MatchService` now has `StartPriorityDispatch` / `runPriorityDispatch`:
    - Loads the request (`pickup_lat/lng`, `radius_km`, `status`) and returns immediately if not `PENDING`.
    - Selects eligible drivers (active, with recent `last_seen_at` or NULL, with location, application filled when columns exist, and **no WAITING/STARTED trip**), inside the configured radius, ordered by distance.
    - For each driver:
      - Skips if already notified for that request (`request_notifications` row exists).
      - Sends a single “Yangi so'rov (X km uzoqda). Qabul qilasizmi?” message with inline Accept button.
      - Inserts into `request_notifications` with `status = SENT`.
      - Waits up to 8 seconds, polling `ride_requests.status` every second; stops if request becomes `ASSIGNED` or `EXPIRED`.
      - If still `PENDING` after 8 seconds, marks that driver’s notification as `TIMEOUT` and continues to next driver.
  - `BroadcastRequest` is now just a thin wrapper that starts priority dispatch (used by rider creation and radius expansion worker).
- **DB Migration:** `004_dispatch_and_cancel.sql`:
  - Upgrades `request_notifications` to:
    - `id` (INTEGER PRIMARY KEY AUTOINCREMENT).
    - `status` with CHECK: `SENT`, `ACCEPTED`, `REJECTED`, `TIMEOUT`.
  - Extends `trips.status` CHECK to allow `CANCELLED_BY_DRIVER`, `CANCELLED_BY_RIDER`.
- **Files:** `db/migrations/004_dispatch_and_cancel.sql`, `internal/services/match_service.go`

---

## 17. Atomic Accept (Race-Free Driver Assignment)

- **Goal:** Make sure **only one driver** can accept a request. If two drivers press Accept at the same time, only the first wins.
- **Implementation:**
  - `AssignmentService.TryAssign`:
    - Uses a single atomic SQL statement:
      - `UPDATE ride_requests SET status='ASSIGNED', assigned_driver_user_id=?, assigned_at=now() WHERE id=? AND status='PENDING' AND expires_at > now()`.
    - Checks `RowsAffected()`:
      - `0` → another driver already accepted, or request expired → returns `(assigned=false)`.
      - `>0` → success: loads `rider_user_id`, marks this driver’s notification as `ACCEPTED`, creates a `WAITING` trip, and notifies the rider with driver details (phone, car, color, plate).
    - Notifies **other drivers** who were notified for that request: “So'rov allaqachon olindi”.
- **Files:** `internal/services/assignment_service.go`

---

## 18. Ride Request Expiration Worker (Every 5 Seconds)

- **Goal:** Expire requests that wait too long without a driver, and notify the rider.
- **Implementation:**
  - `ride_requests` has `expires_at` (set at creation to `created_at + REQUEST_EXPIRES_SECONDS`, default 60s).
  - `AssignmentService.RunExpiryWorker`:
    - Ticker every **5 seconds**.
    - Inside a transaction, updates all `PENDING` requests with `expires_at <= now()` to `EXPIRED` and returns their `id` and `rider_user_id`.
    - After commit, notifies each rider once with **“Haydovchi topilmadi.”** (simple failure message).
- **Files:** `internal/services/assignment_service.go`

---

## 19. Trip Cancellation (Driver & Rider) + Bot “Bekor qilish”

- **Goal:** Support real trip cancellation with proper statuses and UX:
  - **Driver** can cancel a trip already accepted/started.
  - **Rider** can cancel the trip via “Bekor qilish” even after a driver has accepted (“safarni bekor qilish”).
- **Implementation (Backend):**
  - **Statuses:**
    - `CANCELLED_BY_DRIVER`, `CANCELLED_BY_RIDER` added to trip statuses and DB CHECK.
  - **Service:**
    - `TripService.CancelByDriver(tripID, driverUserID)`:
      - Uses `TripRepo.CancelByDriver` to set status to `CANCELLED_BY_DRIVER` only when status is `WAITING` or `STARTED`.
      - Sets `drivers.is_active = 1` (driver available again).
      - Notifies rider: “Haydovchi safarni bekor qildi.”
      - Broadcasts `trip_cancelled` via WebSocket with `{"by": "driver"}`.
    - `TripService.CancelByRider(tripID, riderUserID)`:
      - Uses `TripRepo.CancelByRider` to set status to `CANCELLED_BY_RIDER` when status is `WAITING` or `STARTED`.
      - Sets `drivers.is_active = 1` and notifies driver: “Mijoz safarni bekor qildi.”
      - Broadcasts `trip_cancelled` via WebSocket with `{"by": "rider"}`.
  - **Repository (avoid UPDATE...RETURNING issues):**
    - `TripRepo.CancelByDriver/CancelByRider`:
      - `Exec` the `UPDATE ... WHERE status IN ('WAITING','STARTED')`.
      - Check `RowsAffected()`; if `0`, nothing to cancel.
      - Then `SELECT rider_user_id` or `driver_user_id` in a separate query (works even if driver doesn’t support `UPDATE ... RETURNING`).
  - **HTTP:**
    - `POST /trip/cancel/driver` with `{"trip_id","driver_id"}` → driver cancel.
    - `POST /trip/cancel/rider` with `{"trip_id","rider_id"}` → rider cancel.
- **Implementation (Rider bot “❌ Bekor qilish”):**
  - `handleCancel` now:
    1. Tries to find an active trip (`trips` with this `rider_user_id` and status `WAITING` or `STARTED`).
    2. If found, calls `TripService.CancelByRider`; on success sends “Safar bekor qilindi.”, then shows phone + location prompts so rider can create a new request.
    3. If no active trip, falls back to old behaviour: cancel the latest `PENDING` `ride_requests` row and show “Bekor qilindi.”.
- **Files:** `db/migrations/004_dispatch_and_cancel.sql`, `internal/domain/types.go`, `internal/repositories/trip_repo.go`, `internal/services/trip_service.go`, `internal/handlers/trip.go`, `internal/bot/rider/bot.go`

---

## 20. WebSocket `/ws` for Live Trip Updates

- **Goal:** Allow clients (e.g. Mini App) to subscribe to live trip events with a reliable event model.
- **Event model:** Each event has `type`, `trip_id`, `trip_status`, `emitted_at` (RFC3339), and optional `payload`.
- **Events:**
  - **`trip_started`** – Payload: `trip_status`, `distance_m: 0`, `distance_km: 0`, `fare` (base) so frontend can resync.
  - **`trip_finished`** – Payload: `trip_status`, `distance_m`, `distance_km`, `fare_amount`, `fare` (final summary).
  - **`driver_location_update`** – Only while trip is STARTED. Payload: `lat`, `lng`, `distance_km`, `fare` (live from current `trips.distance_m`).
  - **`trip_cancelled`** – Payload: `{"by": "driver"}` or `{"by": "rider"}`.
- **Implementation:**
  - Hub sets `EmittedAt` if empty; broadcast is skipped when trip is not STARTED for location updates.
  - GET /trip/:id can be used after any event for full resync; payloads expose enough for clean UI updates.
- **Files:** `internal/ws/hub.go`, `internal/ws/handler.go`, `internal/server/server.go`, `internal/services/trip_service.go`, `internal/handlers/driver_location.go`

---

## 21. Driver Location Filtering (Accuracy, Min Movement, Min Speed)

- **Goal:** Reduce GPS noise so distance and fare are stable and realistic.
- **Implementation:**
  - **HTTP handler (`POST /driver/location`):**
    - Body now accepts optional `accuracy` (meters).
    - If `accuracy > 50`, ignores the update and returns `{"ok": true, "ignored": "accuracy too low"}`.
  - **TripService.AddPoint:**
    - Only processes points when trip status is `STARTED`.
    - Loads previous point (`lat, lng, ts`).
    - Ignores movement **< 5 meters** (tiny jitter).
    - Computes segment speed in km/h using distance and time between points.
    - **Only adds distance to `trips.distance_m` if speed > 2 km/h**.
    - All distances use Haversine via `utils.HaversineMeters`.
  - `trip_locations` keeps the raw points and timestamps for full path history.
- **Files:** `internal/handlers/driver_location.go`, `internal/services/trip_service.go`, `db/migrations/001_init.sql` (trip_locations schema), `internal/utils/geo.go`

---

## 22. Backend Fare Calculation (Server-Side Only)

- **Goal:** Move fare logic entirely to the backend; frontend only displays. **Trips.distance_m** accumulates during STARTED; **GET /trip/:id** always returns live distance and fare.
- **Config:** `STARTING_FEE` (BASE_FARE), `PRICE_PER_KM` (PER_KM_FARE) in `internal/config/config.go`.
- **Distance accumulation:**
  - **TripRepo.AddTripDistance(ctx, tripID, segmentMeters)** – `UPDATE trips SET distance_m = distance_m + ? WHERE id = ? AND status = 'STARTED'` (only STARTED trips).
  - **TripService.AddPoint** – Only when trip is STARTED; inserts into `trip_locations`; when segment valid (movement ≥ 5 m, speed > 2 km/h) calls `AddTripDistance`. Uses robust `parseTripLocationTime` for DB `ts` and duration fallback so distance is not dropped on time parse issues.
- **FinishTrip:** Reads current `distance_m` from trip row, computes **raw** fare (FareService or config); then **normalizes** fare: if raw ≤ 50 so'm → 0, if raw > 50 → round to nearest 100 so'm. Stored `fare_amount` and all displayed fare (rider/driver messages, API) use this **normalized** value. Commission is taken as **x% of normalized fare** (see §33).
- **GET /trip/:id (live response):**
  - Single SELECT: `t.status`, `t.distance_m`, `t.fare_amount` (and pickup/drop/driver/rider). This row is the source of truth.
  - **STARTED:** `distance_km = distance_m/1000` (live accumulated value); `fare` = `CalculateFareRounded(base, perKm, distance_km)`; `fare_amount` in JSON is null.
  - **FINISHED:** Same `distance_m`/`distance_km`; `fare` and `fare_amount` = stored (normalized) `fare_amount`.
  - **TripSummary** nested object: `id`, `status`, `distance_m`, `distance_km`, `fare`, `fare_amount` (null until FINISHED). Top-level fields kept for compatibility.
  - Helper **TripFareForResponse(status, distanceM, fareAmount, baseFare, perKm)** used for consistent fare/pointer logic.
- **Files:** `internal/config/config.go`, `internal/utils/money.go`, `internal/repositories/trip_repo.go`, `internal/services/trip_service.go`, `internal/handlers/trip.go`

---

## 23. Simple Rate Limiting (Rider & Driver)

- **Goal:** Prevent spam and weird states from rapidly repeated actions.
- **Implementation:**
  - **Rider:** Only **1 active PENDING ride_request** at a time.
    - Before creating a new request, `handleLocation` checks for any PENDING request for this rider.
    - If one exists, sends message: “Sizda allaqachon faol so'rov bor. Haydovchi topilguncha yoki bekor qilinguncha kuting.” and does not create another.
  - **Driver:** At most **1 request notification per 5 seconds**.
    - `MatchService` tracks `lastDriverNotif[driver_user_id]`.
    - If last notification for that driver was < 5 seconds ago, skips sending another order at that moment.
- **Files:** `internal/bot/rider/bot.go`, `internal/services/match_service.go`

---

## 24. Trip State Machine & Idempotent Actions

- **Goal:** Prevent invalid trip transitions and handle duplicate actions safely; race-condition-safe DB updates.
- **Domain:** `internal/domain/trip_state.go`
  - Allowed: WAITING → STARTED, CANCELLED_*; STARTED → FINISHED, CANCELLED_*.
  - Helpers: `CanTransition`, `ValidateTransition`, `IsTerminal`.
  - Errors: `ErrInvalidTransition`, `ErrTripNotFound`, `ErrAlreadyFinished`, `ErrAlreadyCancelled`.
- **Services:** StartTrip, FinishTrip, CancelByDriver, CancelByRider call `ValidateTransition` before updates; idempotent: if already in target state return success with `result: "noop"`.
- **Repository:** Conditional UPDATEs with `WHERE status = ?` (e.g. UpdateToStarted only when WAITING, UpdateToFinished only when STARTED); RowsAffected checked for race handling.
- **Handlers:** Map domain errors to HTTP (404 trip not found, 409 invalid transition); success response: `{"ok": true, "trip_id", "status", "result": "updated"}` or `{"ok": true, "result": "noop"}`.
- **Files:** `internal/domain/trip_state.go`, `internal/services/trip_service.go`, `internal/repositories/trip_repo.go`, `internal/handlers/trip.go`

---

## 25. Cancellation Metadata

- **DB:** Migration `005_cancellation_metadata.sql` adds to `trips`: `cancelled_at`, `cancelled_by`, `cancel_reason`.
- **Repo:** CancelByDriver / CancelByRider set these fields and return the other party’s user_id for notifications.
- **Files:** `db/migrations/005_cancellation_metadata.sql`, `internal/repositories/trip_repo.go`

---

## 26. Structured Logging

- **Goal:** Observable trip and auth events for debugging.
- **Logger:** `internal/logger/logger.go` (log/slog). Events: trip start/finish/cancel, driver location accepted/ignored, WebSocket connect/disconnect, auth failure. Attributes: trip_id, user_id, driver_user_id, rider_user_id, action, result.
- **Usage:** TripService and DriverLocation handler log trip and location outcomes; WebSocket handler logs connect/disconnect and auth failures.
- **Files:** `internal/logger/logger.go`, `internal/services/trip_service.go`, `internal/handlers/driver_location.go`, `internal/ws/handler.go`

---

## 27. Mini App Driver Auth (X-Driver-Id)

- **Goal:** Mini App driver can call Start/Cancel/Finish and POST /driver/location using `X-Driver-Id` (internal user_id) when initData is not used.
- **Middleware:** `TryDriverIDHeader(db)` reads `X-Driver-Id`, verifies user is in `drivers` via `ResolveDriverByUserID`, sets User in context with RoleDriver.
- **RequireDriverAuth:** If User already in context (e.g. set by TryDriverIDHeader), skips its own auth and continues.
- **Server:** Driver routes use `tryDriverID` then `driverAuth`; CORS allows `X-Driver-Id` header.
- **Config:** `ENABLE_DRIVER_ID_HEADER` to enable header-based driver auth.
- **Files:** `internal/auth/miniapp_driver.go`, `internal/auth/middleware.go`, `internal/auth/resolve.go`, `internal/server/server.go`, `internal/config/config.go`, `docs/AUTH.md`

---

## 28. Render Deployment (Auto-Migrations, Single Instance)

- **Migrations:** Dockerfile builds `cmd/migrate` and runs `./migrate -up` before `./app` so the DB schema is applied on container start (fixes “no such table: ride_requests”).
- **Single instance:** `render.yaml` and README set instance count to **1** to avoid “Conflict: terminated by other getUpdates request” (one bot connection per bot).
- **Files:** `Dockerfile`, `render.yaml`, `README.md`

---

## 29. Tests

- **Domain:** `internal/domain/trip_state_test.go` – table-driven tests for CanTransition, ValidateTransition, IsTerminal.
- **Utils:** `internal/utils/money_test.go` – CalculateFareRounded (zero distance, rounding, negative).
- **Handlers:** `internal/handlers/trip_test.go` – TripFareForResponse (STARTED vs FINISHED, stored fare, null fare_amount). `internal/handlers/driver_location_test.go` – IgnoreReasonAccuracy.
- **Services:** `internal/services/trip_service_test.go` – parseTripLocationTime (string, []byte, int64, float64, time.Time, invalid).
- **Repositories:** `internal/repositories/trip_repo_test.go` – AddTripDistance / GetTripDistanceAndFare / GetStatus when `TEST_DATABASE_URL` is set (skipped otherwise).

---

## 30. Architecture & Code Organization (MVP)

- **Goal:** Move toward a clean, production-ready layout without microservices (single Go binary).
- **Structure:**
  - `internal/handlers/` – HTTP handlers only: bind/validate, call services, return JSON.
  - `internal/services/` – Business logic (matching, assignment, trips, expiry, WebSocket events).
  - `internal/repositories/` – DB access; e.g. `TripRepo` for trip status updates.
  - `internal/models/` – Core structs (`Trip`, `RideRequest`) decoupled from transport.
  - `internal/ws/` – WebSocket hub and `/ws` handler.
  - `internal/bot/` – Rider and driver Telegram bots (thin; call services/DB).
  - `internal/config/` – Environment-driven configuration (starting fee, per-km fare, timeouts, radiuses).
- **Files:** `internal/models/*.go`, `internal/repositories/trip_repo.go`, `internal/ws/*.go`, `internal/services/*.go`, `internal/handlers/*.go`, `cmd/app/main.go`
---

## 31. Recent Dispatch & Driver Availability Improvements

- **Batched smart dispatch:**
  - Grid + radius filter, then sort drivers by distance.
  - Dispatch in batches of up to 3 nearest drivers (`dispatchBatchSize`), with a 60s acceptance timeout per batch (`dispatchBatchWaitSec` / `DISPATCH_WAIT_SECONDS`).
  - If no one in the batch accepts and the request is still `PENDING`, mark their notifications as `TIMEOUT` and automatically send to the next batch.
  - All existing protections remain: `request_notifications` duplicate guard, cooldown per driver, request TTL, and idempotent accept logic.
- **Dynamic max radius:**
  - Initial radius `MATCH_RADIUS_KM` (default 3 km) stored in `ride_requests.radius_km` at creation.
  - After `RADIUS_EXPANSION_MINUTES`, `AssignmentService.RunRadiusExpansionWorker` expands to `EXPANDED_RADIUS_KM` (now default 4 km) and re-broadcasts.
- **Admin-controlled commission percent:**
  - `fare_settings` table extended with `commission_percent`.
  - `FareService` exposes `UpdateCommissionPercent`; Admin bot gets a new menu item “📊 Komissiya %” to edit it.
  - `TripService.FinishTrip` reads `commission_percent` from DB (or config fallback) when `InfiniteDriverBalance=false`, deducts commission from `drivers.balance`, and records a `payments` row of type `commission`.
- **Driver availability + manual_offline + location-based auto-online:**
  - `drivers.manual_offline` (migration `010_driver_manual_offline.sql`) controls whether location can auto-reactivate a driver.
  - Driver bot:
    - **Offline button** → `is_active=0`, `manual_offline=1`.
    - **Online button** → `is_active=1`, `manual_offline=0`, `last_seen_at` updated, and `NotifyDriverOfPendingRequests` triggered.
    - **Manual location (`message.location`)**:
      - Always updates `last_lat`, `last_lng`, `last_seen_at`, `grid_id`.
      - If no WAITING/STARTED trip and `manual_offline=0`, sets `is_active=1` (same as pressing Online) and runs `NotifyDriverOfPendingRequests`.
      - If already active and not manually offline, runs `NotifyDriverOfPendingRequests` when movement ≥ 0.3 km.
    - **Live location (`edited_message.location`)**:
      - Always updates `last_lat`, `last_lng`, `last_seen_at`, `grid_id`.
      - If `is_active=0` and `manual_offline=0`, sets `is_active=1` and runs `NotifyDriverOfPendingRequests` once (no chat reply).
      - If `manual_offline=1`, only stores the position; no reactivation, no dispatch.
      - If active and not manually offline, runs `NotifyDriverOfPendingRequests` when movement ≥ 300 m (silent).
  - Trip service:
    - When driver or rider cancels, `is_active` is set to 1 **only if** `manual_offline=0` (`CASE WHEN COALESCE(manual_offline,0)=0 THEN 1 ELSE is_active END`), so manual offline is respected.
    - On finish, driver is set `is_active=0` and prompted to share location; the next location (if not manual offline) auto-reactivates and triggers dispatch.

---

## 32. Driver Bot UX: Keyboard, Live Location Reminders, Dispatch Freshness

- **Driver keyboard (single row, status-dependent):**
  - **Offline:** **📡 Jonli lokatsiya yoqish** | **🟢 Ishni boshlash**
  - **Online:** **📡 Jonli lokatsiya yoqish** | **🔴 Offlinega o'tish**
  - Shown after application, after location, after trip finish; no OneTimeKeyboard so the keyboard stays.
- **Onboarding on /start (registered driver):**
  - Single message: "🚕 YettiQanot Haydovchi" + "Buyurtmalar olish uchun 2 ta qadam kerak: 1️⃣ Online bo'lish 2️⃣ Jonli lokatsiyani yoqish" with the keyboard above (Ishni boshlash when offline).
- **Button "🟢 Ishni boshlash" (and "🟢 Onlinega o'tish"):** Both set driver online; keyboard switches to Offline.
- **Button "📡 Jonli lokatsiya yoqish":**
  - Sends Live Location instruction (📎 → Location → Share Live Location, 8 soat). If already sharing live, no message (no spam). Cooldown 3 min for repeated presses.
- **Online but Live Location NOT active:**
  - Full reminder (once per 8h): "📡 Siz onlinesiz, lekin jonli lokatsiya yoqilmagan." + 4 steps (📎, Геопозиция/Location, Share Live Location, 8 soat). Short message within cooldown: "🟢 Siz onlinesiz. Buyurtmalar olish uchun jonli lokatsiyani yoqing." + bilingual instruction. Never says "So'rovlar keladi" when live is off.
- **Live Location detected:** Once: "📡 Jonli lokatsiya qabul qilindi. Endi sizga yaqin buyurtmalar keladi."
- **Live Location stopped:** Once: "📍 Jonli lokatsiya o'chdi. Buyurtmalar kelmaydi. Qayta yoqish uchun: …"
- **/status:** "📊 Haydovchi holati" + Holat (🟢 Online / 🔴 Offline), Lokatsiya (📡 yoqilgan / ❌ yoqilmagan), Balans (so'm). Reminders only on state change; no duplicate messages.
- **Dispatch:** Only **Telegram Live Location** is accepted for dispatch; static location is rejected with a message. Eligibility: `live_location_active = 1` and `last_live_location_at` within 90 s; balance and location freshness unchanged.
- **Files:** `internal/bot/driver/bot.go`, `internal/services/trip_service.go`, `internal/services/match_service.go`

---

## 33. Normalized Fare and Commission (x% of Fare)

- **Normalized fare (display and storage):**
  - Raw fare is computed from distance (FareService or config) as before.
  - **Normalize:** If raw fare ≤ 50 so'm → **0**; if raw fare > 50 so'm → **round to nearest 100** so'm. This normalized value is stored in `trips.fare_amount` and shown in rider/driver messages and API.
- **Commission:** Taken as **x% of the normalized fare** (integer: `fareAmount * percent / 100`). No rounding to 10 so'm. Percent comes from fare_settings or config (default 5%). When `INFINITE_DRIVER_BALANCE=true`, commission is not deducted and all drivers receive orders.
- **Files:** `internal/services/trip_service.go` (`normalizeFare`, `FinishTrip`)

---

## 34. Admin Payments API: total_price

- **Goal:** Admin frontend can show "Total Price" per payment (trip total for commission rows).
- **Implementation:**
  - **Migration 016:** `payments.trip_id` (nullable TEXT) links commission payment to trip.
  - **TripService:** When recording commission, INSERT includes `trip_id` so new commission rows are linked.
  - **ListPayments:** Query uses `LEFT JOIN trips t ON p.trip_id = t.id`; for each payment, `t.fare_amount` is exposed as **total_price** (number) in the JSON response. For non-trip payments (deposit, adjustment) or old rows without `trip_id`, `total_price` is omitted (null).
  - **Endpoints:** `GET /admin/payments` and `GET /admin/payments?driver_id=<id>` unchanged in filtering/sorting; only the new field is added. Existing fields (id, driver_id, amount, type, note, created_at) unchanged.
- **Files:** `db/migrations/016_payments_trip_id.sql`, `internal/models/payment.go` (TotalPrice, TripID), `internal/repositories/payment_repository.go`, `internal/services/trip_service.go`, `internal/handlers/admin_handlers.go`

---

This is the updated full plan of what we have done: smart batched dispatch with distance-based ordering and 90s location freshness, trip cancellation and state machine, distance/fare accumulation and live GET `/trip/:id`, **normalized fare** (≤50→0, >50→nearest 100) and **commission as x% of fare**, WebSocket payloads, Mini App driver auth, Render deployment, tests, admin-controlled tariffs/commission, **admin payments API with total_price**, live-location-only dispatch and driver onboarding UX (onboarding message, Ishni boshlash, live detected/stopped messages, status, no duplicate reminders), and single trip completion message for driver.
