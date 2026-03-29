package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/logger"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
	"taxi-mvp/internal/ws"
)

// DriverLocationAccuracyMaxMeters is the maximum accuracy (meters) to accept; above this location updates are ignored.
const DriverLocationAccuracyMaxMeters = 50

// IgnoreReasonAccuracy returns the ignore reason when accuracy is too low, or empty string when acceptable.
func IgnoreReasonAccuracy(accuracy float64) string {
	if accuracy > 0 && accuracy > DriverLocationAccuracyMaxMeters {
		return "accuracy too low"
	}
	return ""
}

// DriverLocationRequest is the JSON body for POST /driver/location. driver_id comes from auth context.
type DriverLocationRequest struct {
	Lat       float64 `json:"lat" binding:"required"`
	Lng       float64 `json:"lng" binding:"required"`
	Accuracy  float64 `json:"accuracy"`  // meters; optional, ignored if > 50
	Timestamp *int64  `json:"timestamp"` // Unix seconds when fix was taken; optional, else server time
}

// DriverLocation updates driver's last position and optionally adds a point to active trip.
// Returns {"ok": true} or {"ok": true, "ignored": "reason"} when update is ignored. Broadcasts driver_location_update with lat, lng, distance_km, fare when trip is STARTED.
// When driver has no active (WAITING/ARRIVED/STARTED) trip and is not manually offline, also marks driver available again
// and runs pending-request dispatch (for example right after finishing a trip from the Mini App).
func DriverLocation(db *sql.DB, tripSvc *services.TripService, matchSvc *services.MatchService, driverBot *tgbotapi.BotAPI, hub *ws.Hub, cfg *config.Config, fareSvc *services.FareService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			logger.AuthFailure("driver auth required")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		var req DriverLocationRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ctx := c.Request.Context()
		driverID := u.UserID
		legalSvc := legal.NewService(db)
		var activeTrip string
		_ = db.QueryRowContext(ctx, `
			SELECT id FROM trips WHERE driver_user_id = ?1 AND status IN ('WAITING','ARRIVED','STARTED') LIMIT 1`,
			driverID).Scan(&activeTrip)
		if activeTrip == "" && !legalSvc.DriverHasActiveLegal(ctx, driverID) {
			c.JSON(http.StatusForbidden, gin.H{"error": legal.ErrCodeRequired})
			return
		}
		if reason := IgnoreReasonAccuracy(req.Accuracy); reason != "" {
			logger.DriverLocation("", driverID, "ignored", reason)
			c.JSON(http.StatusOK, gin.H{"ok": true, "ignored": reason})
			return
		}
		var incomingTime time.Time
		if req.Timestamp != nil && *req.Timestamp > 0 {
			incomingTime = time.Unix(*req.Timestamp, 0).UTC()
		} else {
			incomingTime = time.Now().UTC()
		}
		var lastSeenAt sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT last_seen_at FROM drivers WHERE user_id = ?1`, driverID).Scan(&lastSeenAt)
		stale := false
		if lastSeenAt.Valid && lastSeenAt.String != "" {
			if parsed, err := time.Parse("2006-01-02 15:04:05", lastSeenAt.String); err == nil && !incomingTime.After(parsed) {
				logger.DriverLocation("", driverID, "ignored", "stale")
				stale = true
			}
		}
		if !stale {
			nowStr := incomingTime.Format("2006-01-02 15:04:05")
			gridID := utils.GridID(req.Lat, req.Lng)
			if cfg != nil && cfg.DispatchDebug {
				logger.DriverLocation("", driverID, "grid_update", "grid_id="+gridID)
			}
			_, _ = db.ExecContext(ctx, `
				UPDATE drivers SET last_lat = ?1, last_lng = ?2, last_seen_at = ?3, grid_id = ?4 WHERE user_id = ?5`,
				req.Lat, req.Lng, nowStr, gridID, driverID)
		}
		var tripID string
		var ignoredReason string
		if tripSvc != nil {
			err := db.QueryRowContext(ctx, `SELECT id FROM trips WHERE driver_user_id = ?1 AND status = ?2 LIMIT 1`,
				driverID, domain.TripStatusStarted).Scan(&tripID)
			if err == nil && tripID != "" {
				accepted, reason, addErr := tripSvc.AddPoint(ctx, tripID, driverID, req.Lat, req.Lng, time.Now())
				if addErr != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add point"})
					return
				}
				if !accepted && reason != "" {
					ignoredReason = reason
				}
				if accepted {
					logger.DriverLocation(tripID, driverID, "accepted", "")
					// Only broadcast if trip is still STARTED; include live distance_km and fare for frontend
					var status string
					var distanceM int64
					_ = db.QueryRowContext(ctx, `SELECT status, distance_m FROM trips WHERE id = ?1`, tripID).Scan(&status, &distanceM)
					if hub != nil && status == domain.TripStatusStarted {
						payload := map[string]interface{}{"lat": req.Lat, "lng": req.Lng}
						distanceKm := float64(distanceM) / 1000
						payload["distance_km"] = distanceKm
						if fareSvc != nil {
							if fare, err := fareSvc.CalculateFare(ctx, distanceKm); err == nil {
								payload["fare"] = fare
							}
						} else if cfg != nil {
							payload["fare"] = utils.CalculateFareRounded(float64(cfg.StartingFee), float64(cfg.PricePerKm), distanceKm)
						}
						hub.BroadcastToTrip(tripID, ws.Event{
							Type:       "driver_location_update",
							TripStatus: domain.TripStatusStarted,
							Payload:    payload,
						})
					}
				}
			}
		}
		// Mini App location updates grid/trip only. Dispatch and "online" for drivers use Telegram live location, not this endpoint.

		if ignoredReason != "" {
			c.JSON(http.StatusOK, gin.H{"ok": true, "ignored": ignoredReason})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
