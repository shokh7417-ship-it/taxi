package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

// TripFareForResponse returns fare (display) and fareAmount (nil until FINISHED). For FINISHED trips uses stored fare_amount; otherwise uses computedFare (tiered or legacy).
func TripFareForResponse(status string, fareAmount sql.NullInt64, computedFare int64) (fare int64, fareAmountPtr *int64) {
	if fareAmount.Valid && status == "FINISHED" {
		v := fareAmount.Int64
		return v, &v
	}
	return computedFare, nil
}

// writeTripError maps domain errors to HTTP status and JSON error response.
func writeTripError(c *gin.Context, tripID string, err error) {
	switch {
	case errors.Is(err, domain.ErrTripNotFound):
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "trip not found", "trip_id": tripID})
	case errors.Is(err, domain.ErrInvalidTransition):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "invalid transition", "trip_id": tripID})
	case errors.Is(err, domain.ErrAlreadyFinished), errors.Is(err, domain.ErrAlreadyCancelled):
		c.JSON(http.StatusConflict, gin.H{"ok": false, "error": err.Error(), "trip_id": tripID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "operation failed", "trip_id": tripID})
	}
}

// writeTripResult writes success response: for noop {"ok": true, "result": "noop"}, for updated includes trip_id and status.
func writeTripResult(c *gin.Context, tripID string, result *services.TripActionResult) {
	if result == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "result": "noop"})
		return
	}
	if result.Result == "noop" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "result": "noop"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "trip_id": tripID, "status": result.Status, "result": result.Result})
}

// TripStartRequest body for POST /trip/start. driver_id comes from auth context.
type TripStartRequest struct {
	TripID string `json:"trip_id" binding:"required"`
}

// TripFinishRequest body for POST /trip/finish. driver_id comes from auth context.
type TripFinishRequest struct {
	TripID string `json:"trip_id" binding:"required"`
}

// TripCancelDriverRequest body for POST /trip/cancel/driver. driver_id comes from auth context.
type TripCancelDriverRequest struct {
	TripID string `json:"trip_id" binding:"required"`
}

// TripCancelRiderRequest body for POST /trip/cancel/rider. rider_id comes from auth context.
type TripCancelRiderRequest struct {
	TripID string `json:"trip_id" binding:"required"`
}

// LatLng is a point for rider/driver Mini App (pickup, drop, driver position).
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// TripSummary is the standardized trip object for resync (nested in GET /trip/:id; rider and driver Mini App).
type TripSummary struct {
	ID         string  `json:"id"`          // trip id (string; e.g. UUID)
	Status     string  `json:"status"`      // WAITING | STARTED | FINISHED | CANCELLED_*
	DistanceM  int64   `json:"distance_m,omitempty"`
	DistanceKm float64 `json:"distance_km"`
	Fare       int64   `json:"fare"`        // current estimate or final stored amount
	FareAmount *int64  `json:"fare_amount,omitempty"` // null until FINISHED
}

// TripInfoResponse is returned by GET /trip/:id for Mini App (rider: track driver; driver: run trip).
// Rider-friendly: trip, pickup, drop, driver as objects; driver_info for display.
type TripInfoResponse struct {
	TripID     string       `json:"trip_id"`
	DriverID   int64        `json:"driver_id,omitempty"`
	Status     string       `json:"status"`
	Pickup     LatLng       `json:"pickup"`   // { lat, lng } for rider/driver map
	Drop       LatLng       `json:"drop"`     // { lat, lng }
	Driver     LatLng       `json:"driver"`   // { lat, lng } from drivers.last_lat/lng
	DistanceKm float64      `json:"distance_km"`
	Fare       int64        `json:"fare"`
	Trip       *TripSummary `json:"trip,omitempty"`
	DriverInfo *struct {
		Phone   string `json:"phone,omitempty"`
		CarType string `json:"car_type,omitempty"`
		Color   string `json:"color,omitempty"`
		Plate   string `json:"plate,omitempty"`
	} `json:"driver_info,omitempty"` // who is coming to pick up the rider
	// Rider (client) info for driver mini app: show who to pick up and call
	RiderPhone string `json:"rider_phone,omitempty"`
	RiderName  string `json:"rider_name,omitempty"`
	RiderInfo  *struct {
		Phone string `json:"phone,omitempty"`
		Name  string `json:"name,omitempty"`
	} `json:"rider_info,omitempty"`
}

// TripStart calls TripService.StartTrip. Requires driver auth; driver may only start their assigned trip.
func TripStart(db *sql.DB, tripSvc *services.TripService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		var req TripStartRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ctx := c.Request.Context()
		ok, err := auth.AuthorizeTripAccess(ctx, db, u.UserID, req.TripID, u.Role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization failed"})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "not assigned to this trip"})
			return
		}
		result, err := tripSvc.StartTrip(ctx, req.TripID, u.UserID)
		if err != nil {
			writeTripError(c, req.TripID, err)
			return
		}
		writeTripResult(c, req.TripID, result)
	}
}

// TripFinish calls TripService.FinishTrip. Requires driver auth; driver may only finish their assigned trip.
func TripFinish(db *sql.DB, tripSvc *services.TripService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		var req TripFinishRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ctx := c.Request.Context()
		ok, err := auth.AuthorizeTripAccess(ctx, db, u.UserID, req.TripID, u.Role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization failed"})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "not assigned to this trip"})
			return
		}
		result, err := tripSvc.FinishTrip(ctx, req.TripID, u.UserID)
		if err != nil {
			writeTripError(c, req.TripID, err)
			return
		}
		writeTripResult(c, req.TripID, result)
	}
}

// TripCancelDriver calls TripService.CancelByDriver. Requires driver auth; driver may only cancel their assigned trip.
func TripCancelDriver(db *sql.DB, tripSvc *services.TripService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleDriver {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "driver auth required"})
			return
		}
		var req TripCancelDriverRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ctx := c.Request.Context()
		ok, err := auth.AuthorizeTripAccess(ctx, db, u.UserID, req.TripID, u.Role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization failed"})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "not assigned to this trip"})
			return
		}
		result, err := tripSvc.CancelByDriver(ctx, req.TripID, u.UserID)
		if err != nil {
			writeTripError(c, req.TripID, err)
			return
		}
		writeTripResult(c, req.TripID, result)
	}
}

// TripCancelRider calls TripService.CancelByRider. Requires rider auth; rider may only cancel their own trip.
func TripCancelRider(db *sql.DB, tripSvc *services.TripService) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.Role != domain.RoleRider {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "rider auth required"})
			return
		}
		var req TripCancelRiderRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		ctx := c.Request.Context()
		ok, err := auth.AuthorizeTripAccess(ctx, db, u.UserID, req.TripID, u.Role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "authorization failed"})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "not your trip"})
			return
		}
		result, err := tripSvc.CancelByRider(ctx, req.TripID, u.UserID)
		if err != nil {
			writeTripError(c, req.TripID, err)
			return
		}
		writeTripResult(c, req.TripID, result)
	}
}

// TripInfo returns trip details for Mini App. Uses FareService for tiered fare when set; otherwise config. FINISHED uses stored fare_amount.
func TripInfo(db *sql.DB, cfg *config.Config, fareSvc *services.FareService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tripID := c.Param("id")
		if tripID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "trip_id required"})
			return
		}
		ctx := c.Request.Context()
		var pickupLat, pickupLng, dropLat, dropLng sql.NullFloat64
		var driverUserID, riderUserID int64
		var status string
		var distanceM int64
		var fareAmount sql.NullInt64
		// Single SELECT: distance_m and fare_amount are the source of truth (live for STARTED, final for FINISHED).
		err := db.QueryRowContext(ctx, `
			SELECT t.status, t.driver_user_id, t.rider_user_id, t.distance_m, t.fare_amount,
			       r.pickup_lat, r.pickup_lng, r.drop_lat, r.drop_lng
			FROM trips t
			JOIN ride_requests r ON r.id = t.request_id
			WHERE t.id = ?1`, tripID).Scan(&status, &driverUserID, &riderUserID, &distanceM, &fareAmount, &pickupLat, &pickupLng, &dropLat, &dropLng)
		if err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		pickup := LatLng{pickupLat.Float64, pickupLng.Float64}
		drop := LatLng{pickupLat.Float64, pickupLng.Float64}
		if dropLat.Valid && dropLng.Valid {
			drop = LatLng{dropLat.Float64, dropLng.Float64}
		}
		var driverLat, driverLng sql.NullFloat64
		var driverPhone, driverCarType, driverColor, driverPlate sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT last_lat, last_lng, phone, car_type, color, plate FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&driverLat, &driverLng, &driverPhone, &driverCarType, &driverColor, &driverPlate)
		driver := LatLng{0, 0}
		if driverLat.Valid && driverLng.Valid {
			driver = LatLng{driverLat.Float64, driverLng.Float64}
		}
		var riderPhone, riderName sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT phone, name FROM users WHERE id = ?1`, riderUserID).Scan(&riderPhone, &riderName)

		distanceKm := float64(distanceM) / 1000
		var computedFare int64
		if fareSvc != nil {
			computedFare, _ = fareSvc.CalculateFare(ctx, distanceKm)
		} else if cfg != nil {
			computedFare = utils.CalculateFareRounded(float64(cfg.StartingFee), float64(cfg.PricePerKm), distanceKm)
		}
		fare, fareAmountPtr := TripFareForResponse(status, fareAmount, computedFare)
		resp := TripInfoResponse{
			TripID:     tripID,
			DriverID:   driverUserID,
			Status:     status,
			Pickup:     pickup,
			Drop:       drop,
			Driver:     driver,
			DistanceKm: distanceKm,
			Fare:       fare,
			Trip: &TripSummary{
				ID:         tripID,
				Status:     status,
				DistanceM:  distanceM,
				DistanceKm: distanceKm,
				Fare:       fare,
				FareAmount: fareAmountPtr,
			},
		}
		if riderPhone.Valid {
			resp.RiderPhone = riderPhone.String
		}
		if riderName.Valid {
			resp.RiderName = riderName.String
		}
		if riderPhone.Valid && riderPhone.String != "" || riderName.Valid && riderName.String != "" {
			resp.RiderInfo = &struct {
				Phone string `json:"phone,omitempty"`
				Name  string `json:"name,omitempty"`
			}{
				Phone: riderPhone.String,
				Name:  riderName.String,
			}
		}
		if driverPhone.Valid && driverPhone.String != "" || driverCarType.Valid && driverCarType.String != "" || driverColor.Valid && driverColor.String != "" || driverPlate.Valid && driverPlate.String != "" {
			resp.DriverInfo = &struct {
				Phone   string `json:"phone,omitempty"`
				CarType string `json:"car_type,omitempty"`
				Color   string `json:"color,omitempty"`
				Plate   string `json:"plate,omitempty"`
			}{
				Phone:   driverPhone.String,
				CarType: driverCarType.String,
				Color:   driverColor.String,
				Plate:   driverPlate.String,
			}
		}
		c.JSON(http.StatusOK, resp)
	}
}
