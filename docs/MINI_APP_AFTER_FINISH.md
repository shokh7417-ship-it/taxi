# Mini App: Send location after "SAFARNI TUGATISH"

Your driver mini app (e.g. **https://mini-app-front-ten.vercel.app/**) must send the driver's current location to the backend **right after** finishing a trip so the driver becomes available for new orders without pressing Online.

## Backend (this repo) — already done

- On trip finish we set only `is_active = 0` (we do **not** set `manual_offline = 1`).
- When `POST /driver/location` is received:
  - We update `last_lat`, `last_lng`, `last_seen_at`, `grid_id`.
  - If the driver has **no active trip** (no WAITING or STARTED) and **manual_offline = 0**, we set `is_active = 1`, run `NotifyDriverOfPendingRequests`, and send the Telegram message: **"Lokatsiya yangilandi. Sizga buyurtmalar keladi."**

So as soon as the mini app sends location after finish, the driver is marked active and gets pending requests.

## Frontend (Vercel mini app) — what to add

When the user presses **"SAFARNI TUGATISH"**:

1. Call `POST <API_BASE>/trip/finish` with body `{ "trip_id": "<trip_id>" }` and header `X-Driver-Id: <driver_id>` (same as now).
2. **Right after** a successful response (e.g. `result === "updated"` or `ok === true`), get the current GPS position and call the location endpoint:
   - `POST <API_BASE>/driver/location`
   - Headers: `Content-Type: application/json`, `X-Driver-Id: <driver_id>`
   - Body: `{ "lat": <latitude>, "lng": <longitude>, "accuracy": <meters optional> }`

Use the same `API_BASE` and same `driver_id` / `trip_id` you already use for finish (e.g. from URL params `?trip_id=...&driver_id=...`).

### Example (paste into your mini app)

```javascript
// After POST /trip/finish succeeds (e.g. data.ok && data.result === 'updated'):
function sendLocationAfterFinish(apiBase, driverId, onDone) {
  if (!navigator.geolocation) {
    if (onDone) onDone(new Error('Geolocation not supported'));
    return;
  }
  navigator.geolocation.getCurrentPosition(
    function (pos) {
      fetch(apiBase + '/driver/location', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Driver-Id': String(driverId)
        },
        body: JSON.stringify({
          lat: pos.coords.latitude,
          lng: pos.coords.longitude,
          accuracy: pos.coords.accuracy || 0
        })
      })
        .then(function (r) { return r.json(); })
        .then(function (data) {
          if (onDone) onDone(null, data);
        })
        .catch(function (err) {
          if (onDone) onDone(err);
        });
    },
    function (err) {
      if (onDone) onDone(err);
    },
    { enableHighAccuracy: true, timeout: 10000, maximumAge: 0 }
  );
}

// In your "SAFARNI TUGATISH" button handler, after finish succeeds:
// sendLocationAfterFinish(API_BASE, driverId, function (err, data) {
//   if (err) { /* show error */ return; }
//   // Optionally show: "Lokatsiya yuborildi — buyurtmalar keladi."
// });
```

### Flow summary

| Step | Who | Action |
|------|-----|--------|
| 1 | Mini app | User taps "SAFARNI TUGATISH" |
| 2 | Mini app | `POST /trip/finish` with `trip_id`, `X-Driver-Id` |
| 3 | Backend | Finish trip, set driver `is_active = 0` only |
| 4 | Mini app | Get GPS → `POST /driver/location` with `lat`, `lng`, `X-Driver-Id` |
| 5 | Backend | Update position, set `is_active = 1`, run dispatch, send "Lokatsiya yangilandi. Sizga buyurtmalar keladi." |
| 6 | Driver | Receives new orders without pressing Online |

Ensure your mini app uses the **same backend API base URL** for both `/trip/finish` and `/driver/location` (and that CORS allows your Vercel origin; the backend already sends permissive CORS with `Access-Control-Allow-Headers: ..., X-Driver-Id`).
