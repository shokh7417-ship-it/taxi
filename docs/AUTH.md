# Authentication and Authorization

## 1. File-by-file plan

| File | Purpose |
|------|---------|
| **internal/auth/verify.go** | `VerifyMiniAppInitData(botToken, initData)` — HMAC-SHA256 verification of Telegram initData; returns `telegram_user_id`. |
| **internal/auth/context.go** | `User` struct (user_id, telegram_user_id, role); `WithUser` / `UserFromContext` for request context. |
| **internal/auth/resolve.go** | `ResolveUserFromTelegramID(ctx, db, telegramID)` → (user_id, role); `AuthorizeTripAccess(ctx, db, userID, tripID, role)` → true if user is driver or rider of trip. |
| **internal/auth/middleware.go** | `RequireMiniAppAuth(db, botToken)`; `RequireDriverAuth(...)` — if user already in context (e.g. from TryDriverIDHeader), continues; else tries initData then X-Driver-Id. `RequireRiderAuth` uses rider bot token only. |
| **internal/auth/miniapp_driver.go** | `TryDriverIDHeader(db)` — Gin middleware: when `X-Driver-Id` is present and valid (user exists in drivers table), sets driver in context. Run before RequireDriverAuth so Mini App Start/Cancel/Finish work without initData. |
| **internal/handlers/trip.go** | Trip handlers take `db`; get `User` from context; use `auth.AuthorizeTripAccess`; call services with `u.UserID` (no driver_id/rider_id from body). Request bodies: only `trip_id`. |
| **internal/handlers/driver_location.go** | Get driver from context; body only `lat`, `lng`, `accuracy` (no `driver_id`). |
| **internal/ws/handler.go** | `ServeWsWithAuth(hub, db, driverToken, riderToken, w, r)` — before upgrade: read initData, verify with either token, resolve user, `AuthorizeTripAccess` for trip_id; 401/403 on failure; then upgrade and register. |
| **internal/server/server.go** | Apply `tryDriverID` then `driverAuth` to POST /driver/location, /trip/start, /trip/finish, /trip/cancel/driver so Mini App requests with `X-Driver-Id` are recognized; `riderAuth` to POST /trip/cancel/rider; GET /ws → `ServeWsWithAuth`. CORS allows `X-Telegram-Init-Data` and `X-Driver-Id`. |

## 2. Helper functions

- **ResolveUserFromTelegramID** — `internal/auth/resolve.go`
- **VerifyMiniAppInitData** — `internal/auth/verify.go`
- **AuthorizeTripAccess** — `internal/auth/resolve.go`

## 3. Protected endpoints

- `POST /trip/start` — driver auth; body `{ "trip_id" }`; driver may only start their assigned trip.
- `POST /trip/finish` — driver auth; body `{ "trip_id" }`; driver may only finish their assigned trip.
- `POST /trip/cancel/driver` — driver auth; body `{ "trip_id" }`.
- `POST /trip/cancel/rider` — rider auth; body `{ "trip_id" }`.
- `POST /driver/location` — driver auth; body `{ "lat", "lng", "accuracy?" }`.
- `GET /ws?trip_id=...` — initData required (header or query); only rider or assigned driver of the trip may connect.

## 4. Example request flow

**Driver starts trip – “Safarni boshlash” (Mini App)**

The backend sets the driver in the auth context so trip start/location/finish/cancel work. Two options:

**Option 1: Telegram initData (recommended)**

1. Mini App has `Telegram.WebApp.initData` (from Telegram when the app is opened).
2. Client sends `POST /trip/start` with header **`X-Telegram-Init-Data: <initData>`** and body `{ "trip_id": "uuid" }`.
3. **Middleware** reads initData, validates it with the **driver bot token** (HMAC-SHA256), gets Telegram `user.id`, maps it to internal `user_id` via `users.telegram_id`, and sets context `User{ user_id, telegram_user_id, role: "driver" }`.
4. Handler gets `User` from context; `AuthorizeTripAccess`; then `tripSvc.StartTrip(ctx, trip_id, user_id)`.
5. Response 200 with trip result. If the Mini App does not send `X-Telegram-Init-Data`, requests return 401 until initData is sent.

**Option 2: X-Driver-Id (optional, only if you trust the Mini App URL)**

1. Set **`ENABLE_DRIVER_ID_HEADER=true`** in the environment.
2. For requests that look like they come from the Mini App (e.g. no init data), the backend can accept header **`X-Driver-Id`** with the internal driver **user_id** (same as `users.id` for the driver).
3. Middleware checks that the user exists and has a row in `drivers`; then sets context with that user and role `driver`.
4. Use this only if the Mini App is served from a trusted origin (e.g. same domain or a known HTTPS Mini App URL). Do not enable in untrusted environments.

**Rider cancels trip**

1. Client (rider) sends `POST /trip/cancel/rider` with `X-Telegram-Init-Data` and body `{ "trip_id": "uuid" }`.
2. Middleware verifies with **rider** bot token, resolves rider user, sets context.
3. Handler checks role is rider and `AuthorizeTripAccess` for (user_id, trip_id, "rider"); then `CancelByRider(ctx, trip_id, user_id)`.

**WebSocket subscribe**

1. Client connects to `GET /ws?trip_id=uuid` with `X-Telegram-Init-Data` (or `init_data=...` in query).
2. **Before upgrade**: verify initData with driver then rider token; resolve user; `AuthorizeTripAccess(user_id, trip_id, role)`.
3. If allowed, upgrade to WebSocket and register client for that trip_id; otherwise 401/403 and connection not upgraded.

**Driver location**

1. `POST /driver/location` with header `X-Telegram-Init-Data` and body `{ "lat": 41.2, "lng": 69.3 }`.
2. Middleware sets driver in context; handler uses `u.UserID` for DB update and AddPoint.
