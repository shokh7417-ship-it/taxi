package handlers

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

// liveLocationFreshSeconds matches MatchService dispatch: last_live_location_at must be within this window.
const liveLocationFreshSeconds = 90

func bool01(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DriverAcceptRequestBody is accepted for POST /driver/accept-request. At least one of trip_id or request_id should be set.
type DriverAcceptRequestBody struct {
	TripID    string `json:"trip_id"`
	RequestID string `json:"request_id"`
}

// DriverAvailableOffer is one pending offer for the driver (same underlying rows as Telegram dispatch).
type DriverAvailableOffer struct {
	RequestID  string  `json:"request_id"`
	TripID     string  `json:"trip_id,omitempty"`
	PickupLat  float64 `json:"pickup_lat"`
	PickupLng  float64 `json:"pickup_lng"`
	DistanceKm float64 `json:"distance_km"`
	RadiusKm   float64 `json:"radius_km"`
	ExpiresAt  string  `json:"expires_at,omitempty"`
}

// DriverAssignedTripStub is optional context for an in-progress assignment (Flutter may call GET /trip/:id for full detail).
type DriverAssignedTripStub struct {
	TripID string `json:"trip_id"`
	Status string `json:"status"`
}

// DriverAvailableRequests returns pending offers (request_notifications SENT + PENDING request) and optional active trip stub.
func DriverAvailableRequests(db *sql.DB, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		ctx := c.Request.Context()
		driverID := u.UserID

		var lastLat, lastLng sql.NullFloat64
		_ = db.QueryRowContext(ctx, `SELECT last_lat, last_lng FROM drivers WHERE user_id = ?1`, driverID).Scan(&lastLat, &lastLng)

		rows, err := db.QueryContext(ctx, `
			SELECT r.id, r.pickup_lat, r.pickup_lng, r.radius_km, COALESCE(r.expires_at,'')
			FROM request_notifications n
			JOIN ride_requests r ON r.id = n.request_id
			WHERE n.driver_user_id = ?1 AND n.status = ?2
			  AND r.status = ?3 AND r.expires_at > datetime('now')`,
			driverID, domain.NotificationStatusSent, domain.RequestStatusPending)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
			return
		}
		defer rows.Close()

		var offers []DriverAvailableOffer
		for rows.Next() {
			var o DriverAvailableOffer
			if err := rows.Scan(&o.RequestID, &o.PickupLat, &o.PickupLng, &o.RadiusKm, &o.ExpiresAt); err != nil {
				continue
			}
			if lastLat.Valid && lastLng.Valid {
				o.DistanceKm = utils.HaversineMeters(lastLat.Float64, lastLng.Float64, o.PickupLat, o.PickupLng) / 1000
			}
			offers = append(offers, o)
		}
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
			return
		}

		var assigned *DriverAssignedTripStub
		var tripID, status string
		err = db.QueryRowContext(ctx, `
			SELECT id, status FROM trips
			WHERE driver_user_id = ?1 AND status IN ('WAITING','ARRIVED','STARTED') LIMIT 1`,
			driverID).Scan(&tripID, &status)
		if err == nil && tripID != "" {
			assigned = &DriverAssignedTripStub{TripID: tripID, Status: status}
		}

		resp := gin.H{
			"assigned_trip":      assigned,
			"available_requests": offers,
			"requests":           offers,
			"pending_requests":   offers,
			"queue":              offers,
			"orders":             offers,
			"jobs":               offers,
		}

		// Debug: explain why queue is empty for a specific driver (without changing Telegram flows).
		if cfg != nil && cfg.DriverAvailableRequestsDebug && len(offers) == 0 {
			if cfg.DriverAvailableRequestsDebugDriverID == 0 || cfg.DriverAvailableRequestsDebugDriverID == driverID {
				// Driver row snapshot
				var (
					isActive, manualOffline, liveLocationActive int
					ver                                         sql.NullString
					lastSeenAt, lastLiveAt                      sql.NullString
					promoBal, cashBal, balance                  sql.NullInt64
				)
				_ = db.QueryRowContext(ctx, `
					SELECT COALESCE(is_active,0), COALESCE(manual_offline,0), COALESCE(live_location_active,0),
					       verification_status, last_seen_at, last_live_location_at,
					       COALESCE(promo_balance,0), COALESCE(cash_balance,0), COALESCE(balance,0)
					FROM drivers WHERE user_id = ?1`, driverID).
					Scan(&isActive, &manualOffline, &liveLocationActive, &ver, &lastSeenAt, &lastLiveAt, &promoBal, &cashBal, &balance)

				// Offer counters
				var notifSent, notifAny int
				_ = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM request_notifications WHERE driver_user_id = ?1 AND status = ?2`,
					driverID, domain.NotificationStatusSent).Scan(&notifSent)
				_ = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM request_notifications WHERE driver_user_id = ?1`,
					driverID).Scan(&notifAny)

				// Pending requests overall
				var pending int
				_ = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM ride_requests WHERE status = ?1 AND expires_at > datetime('now')`,
					domain.RequestStatusPending).Scan(&pending)

				// Eligibility “reasons”
				reasons := make([]string, 0, 8)
				if pending == 0 {
					reasons = append(reasons, "no_pending_requests")
				}
				if notifSent == 0 {
					reasons = append(reasons, "no_sent_offers_for_driver")
				}
				if notifAny == 0 {
					reasons = append(reasons, "no_request_notifications_rows_for_driver")
				}
				if strings.TrimSpace(ver.String) != "approved" {
					reasons = append(reasons, "driver_not_approved")
				}
				if isActive != 1 {
					reasons = append(reasons, "driver_is_active=0")
				}
				if manualOffline == 1 {
					reasons = append(reasons, "driver_manual_offline=1")
				}
				if liveLocationActive != 1 {
					reasons = append(reasons, "live_location_active=0")
				}
				if !cfg.EnableDriverHTTPLiveLocation {
					reasons = append(reasons, "enable_http_live_location_off")
				}
				liveFresh := false
				if lastLiveAt.Valid && strings.TrimSpace(lastLiveAt.String) != "" {
					if t, err := time.ParseInLocation("2006-01-02 15:04:05", lastLiveAt.String, time.UTC); err == nil {
						liveFresh = time.Since(t) <= time.Duration(liveLocationFreshSeconds)*time.Second
					}
				}
				if liveLocationActive == 1 && !liveFresh {
					reasons = append(reasons, "live_location_stale")
				}
				if !cfg.InfiniteDriverBalance && balance.Valid && balance.Int64 <= 0 {
					reasons = append(reasons, "balance_zero_dispatch_ineligible")
				}
				if !legal.NewService(db).DriverHasActiveLegal(ctx, driverID) {
					reasons = append(reasons, "legal_not_accepted")
				}
				seenFresh := false
				if lastSeenAt.Valid && strings.TrimSpace(lastSeenAt.String) != "" {
					if t, err := time.ParseInLocation("2006-01-02 15:04:05", lastSeenAt.String, time.UTC); err == nil {
						seenFresh = time.Since(t) <= time.Duration(cfg.DriverSeenSeconds)*time.Second
					}
				}
				if !seenFresh {
					reasons = append(reasons, "driver_not_seen_recently")
				}

				log.Printf(
					"driver_available_requests_debug driver_id=%d enable_http_live=%v infinite_balance=%v driver_seen_seconds=%d live_fresh_seconds=%d pending_requests=%d notif_sent=%d notif_any=%d is_active=%d manual_offline=%d live_location_active=%d live_fresh=%d ver=%q last_seen_at=%q last_live_at=%q promo=%d cash=%d balance=%d assigned_trip=%v reasons=%s",
					driverID,
					cfg.EnableDriverHTTPLiveLocation,
					cfg.InfiniteDriverBalance,
					cfg.DriverSeenSeconds,
					liveLocationFreshSeconds,
					pending,
					notifSent,
					notifAny,
					isActive,
					manualOffline,
					liveLocationActive,
					bool01(liveFresh),
					strings.TrimSpace(ver.String),
					strings.TrimSpace(lastSeenAt.String),
					strings.TrimSpace(lastLiveAt.String),
					promoBal.Int64,
					cashBal.Int64,
					balance.Int64,
					assigned != nil,
					strings.Join(reasons, ","),
				)
			}
		}
		c.JSON(http.StatusOK, resp)
	}
}

// DriverAcceptRequest delegates to AssignmentService.TryAssign (same as driver bot accept). Schedules start reminder on success.
func DriverAcceptRequest(db *sql.DB, assignSvc *services.AssignmentService, tripSvc *services.TripService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		var req DriverAcceptRequestBody
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		req.RequestID = strings.TrimSpace(req.RequestID)
		req.TripID = strings.TrimSpace(req.TripID)
		if req.RequestID == "" && req.TripID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "trip_id or request_id required"})
			return
		}
		ctx := c.Request.Context()
		driverID := u.UserID

		if req.RequestID == "" && req.TripID != "" {
			var driverUserID int64
			var st string
			err := db.QueryRowContext(ctx, `SELECT driver_user_id, status FROM trips WHERE id = ?1`, req.TripID).Scan(&driverUserID, &st)
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "trip not found", "trip_id": req.TripID})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
				return
			}
			if driverUserID != driverID {
				c.JSON(http.StatusForbidden, gin.H{"ok": false, "error": "not assigned to this trip"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "trip_id": req.TripID, "status": st, "result": "already_assigned"})
			return
		}

		if assignSvc == nil {
			// Admin dashboard uses this path as a "resend/assign" mechanism via X-Driver-Id.
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "match service not configured"})
			return
		}
		assigned, tripID, err := assignSvc.TryAssign(ctx, req.RequestID, driverID)
		if err != nil {
			if errors.Is(err, services.ErrOfferNotFound) {
				c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "offer_not_found", "request_id": req.RequestID})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error(), "request_id": req.RequestID})
			return
		}
		if !assigned {
			// Distinguish unknown request_id (404) from "already taken/expired" (409).
			var exists int
			e := db.QueryRowContext(ctx, `SELECT 1 FROM ride_requests WHERE id = ?1`, req.RequestID).Scan(&exists)
			if e == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "unknown request_id", "request_id": req.RequestID})
				return
			}
			if e != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "lookup failed", "request_id": req.RequestID})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "request no longer available", "request_id": req.RequestID})
			return
		}
		if tripSvc != nil {
			tripSvc.ScheduleStartReminder(ctx, tripID, driverID)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "trip_id": tripID, "request_id": req.RequestID, "assigned": true})
	}
}
