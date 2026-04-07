package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	driverbot "taxi-mvp/internal/bot/driver"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

// AdminHandlers exposes admin HTTP endpoints.
type AdminHandlers struct {
	svc       *services.AdminService
	matchSvc  *services.MatchService
	driverBot *tgbotapi.BotAPI
	db        *sql.DB
}

// NewAdminHandlers creates AdminHandlers. driverBot can be nil; then verify notifications are skipped.
// db is used for legal monitoring and rider list routes; may be nil (those routes are skipped).
// matchSvc can be nil; then GET /admin/nearest-drivers returns 503.
func NewAdminHandlers(svc *services.AdminService, matchSvc *services.MatchService, driverBot *tgbotapi.BotAPI, db *sql.DB) *AdminHandlers {
	return &AdminHandlers{svc: svc, matchSvc: matchSvc, driverBot: driverBot, db: db}
}

// Register registers admin HTTP routes on the given router.
// Mounts the same handlers under /admin, /api/admin, /api/v1/admin, and /v1/admin so dashboards work
// whether API_BASE is the service origin or an /api-prefixed gateway (same pattern as RegisterAdminLegalRoutes).
func (h *AdminHandlers) Register(r *gin.Engine) {
	if h == nil || h.svc == nil {
		return
	}
	for _, base := range []string{"/admin", "/api/admin", "/api/v1/admin", "/v1/admin"} {
		g := r.Group(base)
		h.registerRoutes(g)
	}
}

func (h *AdminHandlers) registerRoutes(g *gin.RouterGroup) {
	g.GET("/drivers", h.ListDrivers)
	g.GET("/map/drivers", h.ListDriversForMap)
	g.GET("/map/ride-requests", h.ListRideRequestsForMap)
	g.POST("/ride-requests/:request_id/offer", h.OfferRideRequestToDriver)
	g.GET("/nearest-drivers", h.NearestDriversForRequest)
	g.GET("/nearest-requests", h.NearestRequestsForDriver)
	g.GET("/drivers/:id/ledger", h.ListDriverLedger)
	g.GET("/riders", h.ListRiders)
	g.POST("/drivers/:id/add-balance", h.AddBalance)
	g.POST("/drivers/:id/adjust-balance", h.AdjustBalance)
	g.POST("/drivers/:id/deduct-balance", h.DeductBalance)
	g.POST("/drivers/:id/verify", h.VerifyDriver)
	g.GET("/payments", h.ListPayments)
	g.GET("/dashboard", h.Dashboard)
}

// ListRiders returns admin rider DTOs (GET /admin/riders).
func (h *AdminHandlers) ListRiders(c *gin.Context) {
	ctx := c.Request.Context()
	riders, err := h.svc.ListRiders(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list riders"})
		return
	}
	c.JSON(http.StatusOK, riders)
}

// ListDrivers returns admin driver DTOs.
func (h *AdminHandlers) ListDrivers(c *gin.Context) {
	ctx := c.Request.Context()
	drivers, err := h.svc.ListDrivers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list drivers"})
		return
	}
	c.JSON(http.StatusOK, drivers)
}

// ListDriversForMap returns only location fields required by the admin map.
func (h *AdminHandlers) ListDriversForMap(c *gin.Context) {
	ctx := c.Request.Context()
	drivers, err := h.svc.ListActiveDriversForMap(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list drivers for map"})
		return
	}
	c.JSON(http.StatusOK, drivers)
}

// ListRideRequestsForMap returns active ride requests for the admin map.
func (h *AdminHandlers) ListRideRequestsForMap(c *gin.Context) {
	ctx := c.Request.Context()
	requests, err := h.svc.ListActiveRideRequestsForMap(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list ride requests for map"})
		return
	}
	c.JSON(http.StatusOK, requests)
}

// NearestDriversForRequest returns dispatch-eligible drivers nearest to a ride request pickup (GET /admin/nearest-drivers?request_id=).
func (h *AdminHandlers) NearestDriversForRequest(c *gin.Context) {
	requestID := strings.TrimSpace(c.Query("request_id"))
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request_id query parameter is required"})
		return
	}
	if h.matchSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "nearest drivers unavailable"})
		return
	}
	drivers, err := h.matchSvc.AdminNearestDispatchDrivers(c.Request.Context(), requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "ride request not found"})
			return
		}
		log.Printf("admin nearest-drivers: request_id=%s err=%v", requestID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list nearest drivers"})
		return
	}
	c.JSON(http.StatusOK, drivers)
}

type offerRideRequestBody struct {
	DriverID int64 `json:"driver_id"`
}

// OfferRideRequestToDriver sends an offer for this request to a specific driver (POST /admin/ride-requests/:request_id/offer).
// This does not auto-assign; it only notifies + records request_notifications so apps can poll.
func (h *AdminHandlers) OfferRideRequestToDriver(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "request_id required"})
		return
	}
	var body offerRideRequestBody
	if err := c.ShouldBindJSON(&body); err != nil || body.DriverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid body"})
		return
	}
	if h.matchSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "offer service not configured"})
		return
	}
	err := h.matchSvc.AdminOfferRequestToDriver(c.Request.Context(), requestID, body.DriverID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrAdminOfferServiceUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "offer service not configured"})
		case errors.Is(err, services.ErrAdminOfferUnknownRequest), errors.Is(err, services.ErrAdminOfferUnknownDriver):
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "unknown request_id or driver_id"})
		case errors.Is(err, services.ErrAdminOfferRequestNotAvail):
			c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "request not available"})
		default:
			log.Printf("admin offer request=%s driver=%d err=%v", requestID, body.DriverID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "failed to send offer"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "sent": true, "request_id": requestID, "driver_id": body.DriverID})
}

type adminNearestRequest struct {
	ID         string  `json:"id"`
	PickupLat  float64 `json:"pickup_lat"`
	PickupLng  float64 `json:"pickup_lng"`
	RadiusKm   float64 `json:"radius_km"`
	ExpiresAt  string  `json:"expires_at"`
	RiderPhone string  `json:"rider_phone"`
	DistanceKm float64 `json:"distance_km"`
}

// NearestRequestsForDriver returns pending ride requests nearest to a driver (GET /admin/nearest-requests?driver_id=).
// This is used by the admin Live Map "Fetch nearest requests" button.
func (h *AdminHandlers) NearestRequestsForDriver(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "db unavailable"})
		return
	}
	driverIDStr := strings.TrimSpace(c.Query("driver_id"))
	driverID, err := strconv.ParseInt(driverIDStr, 10, 64)
	if err != nil || driverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "driver_id query parameter is required"})
		return
	}
	limit := 20
	if s := strings.TrimSpace(c.Query("limit")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	ctx := c.Request.Context()
	var lastLat, lastLng sql.NullFloat64
	if err := h.db.QueryRowContext(ctx, `SELECT last_lat, last_lng FROM drivers WHERE user_id = ?1`, driverID).Scan(&lastLat, &lastLng); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load driver location"})
		return
	}
	if !lastLat.Valid || !lastLng.Valid {
		c.JSON(http.StatusOK, []adminNearestRequest{})
		return
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT r.id, r.pickup_lat, r.pickup_lng, r.radius_km, COALESCE(r.expires_at,''), COALESCE(u.phone,'')
		FROM ride_requests r
		JOIN users u ON u.id = r.rider_user_id
		WHERE r.status = 'PENDING'
		  AND r.expires_at > datetime('now')
		  AND r.pickup_lat IS NOT NULL AND r.pickup_lng IS NOT NULL`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list ride requests"})
		return
	}
	defer rows.Close()

	var out []adminNearestRequest
	for rows.Next() {
		var rr adminNearestRequest
		if err := rows.Scan(&rr.ID, &rr.PickupLat, &rr.PickupLng, &rr.RadiusKm, &rr.ExpiresAt, &rr.RiderPhone); err != nil {
			continue
		}
		rr.DistanceKm = utils.HaversineMeters(lastLat.Float64, lastLng.Float64, rr.PickupLat, rr.PickupLng) / 1000
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list ride requests"})
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DistanceKm < out[j].DistanceKm })
	if len(out) > limit {
		out = out[:limit]
	}
	c.JSON(http.StatusOK, out)
}

type addBalanceRequest struct {
	Amount int64  `json:"amount"` // in smallest currency units (e.g. so'm)
	Note   string `json:"note"`
}

type adjustBalanceRequest struct {
	Amount  int64  `json:"amount"`   // signed delta; positive = credit, negative = debit
	Reason  string `json:"reason"`   // human-readable reason; stored in audit log
	AdminID int64  `json:"admin_id"` // admin user id for audit metadata
}

type deductBalanceRequest struct {
	Amount int64  `form:"amount"`              // parsed from form data when not JSON
	Reason string `json:"reason" form:"reason"` // optional, for audit/log
}

// deductBalanceJSON is used to accept amount as either number or string in JSON.
type deductBalanceJSON struct {
	Amount interface{} `json:"amount"`
	Reason string      `json:"reason"`
}

// VerifyDriverRequest is the body for POST /admin/drivers/:id/verify.
type VerifyDriverRequest struct {
	Status string `json:"status"` // "approved" or "rejected"
}

// VerifyDriver sets driver verification_status and notifies the driver via Telegram.
func (h *AdminHandlers) VerifyDriver(c *gin.Context) {
	idStr := c.Param("id")
	driverID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || driverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver id"})
		return
	}
	var req VerifyDriverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be approved or rejected"})
		return
	}
	telegramID, err := h.svc.SetDriverVerification(c.Request.Context(), driverID, req.Status)
	if err != nil {
		if errors.Is(err, repositories.ErrDriverRejectNotAllowed) {
			c.JSON(http.StatusConflict, gin.H{"error": "driver not found, already approved, or cannot reject"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update verification"})
		return
	}
	if req.Status == "rejected" && h.driverBot != nil && telegramID != 0 {
		driverbot.SendApplicationRejectedMessage(h.driverBot, telegramID)
	}
	c.Status(http.StatusNoContent)
}

// ListDriverLedger returns append-only driver_ledger rows for audit (promo vs cash).
func (h *AdminHandlers) ListDriverLedger(c *gin.Context) {
	idStr := c.Param("id")
	driverID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || driverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver id"})
		return
	}
	limit := 500
	if s := c.Query("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.svc.ListDriverLedger(c.Request.Context(), driverID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list ledger"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"driver_id": driverID, "entries": rows})
}

// AddBalance performs a manual cash-wallet top-up (not promo credit); see README accounting model.
func (h *AdminHandlers) AddBalance(c *gin.Context) {
	idStr := c.Param("id")
	driverID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || driverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver id"})
		return
	}
	var req addBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if req.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be > 0"})
		return
	}
	if err := h.svc.AddDriverBalance(c.Request.Context(), driverID, req.Amount, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add balance"})
		return
	}
	c.Status(http.StatusNoContent)
}

// AdjustBalance applies a signed manual balance delta (credit or debit) and records an audit ledger entry.
// Body: { "amount": <int>, "reason": "...", "admin_id": <int> }.
func (h *AdminHandlers) AdjustBalance(c *gin.Context) {
	idStr := c.Param("id")
	driverID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || driverID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver id"})
		return
	}
	var req adjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if req.Amount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be non-zero"})
		return
	}
	if req.AdminID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "admin_id must be > 0"})
		return
	}
	promo, cash, total, isActive, err := h.svc.AdjustDriverBalance(c.Request.Context(), driverID, req.Amount, req.Reason, req.AdminID)
	if err != nil {
		// Business-rule / not-found mapping.
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":           true,
		"driver_id":         driverID,
		"delta":             req.Amount,
		"promo_balance":     promo,
		"cash_balance":      cash,
		"balance":           total,
		"is_active":         isActive,
		"ledger_entry_type": "MANUAL_ADJUSTMENT",
		"reason":            req.Reason,
		"admin_id":          req.AdminID,
	})
}

// DeductBalance deducts a positive amount from the driver's cash balance only (promo remains unchanged).
// Body: { "amount": <int>, "reason": "..." }.
func (h *AdminHandlers) DeductBalance(c *gin.Context) {
	idStr := c.Param("id")
	driverID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || driverID <= 0 {
		log.Printf("admin_deduct_balance: invalid driver id %q err=%v", idStr, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid driver id"})
		return
	}
	var (
		req deductBalanceRequest
	)

	ct := c.GetHeader("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var j deductBalanceJSON
		if err := c.ShouldBindJSON(&j); err != nil {
			log.Printf("admin_deduct_balance: invalid JSON body driver_id=%d err=%v", driverID, err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		switch v := j.Amount.(type) {
		case float64:
			req.Amount = int64(v)
		case string:
			v = strings.TrimSpace(v)
			if v == "" {
				log.Printf("admin_deduct_balance: missing amount in JSON driver_id=%d", driverID)
				c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be greater than zero"})
				return
			}
			amt, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil {
				log.Printf("admin_deduct_balance: parse amount failed driver_id=%d raw=%q err=%v", driverID, v, perr)
				c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be a valid integer"})
				return
			}
			req.Amount = amt
		case nil:
			log.Printf("admin_deduct_balance: missing amount in JSON driver_id=%d", driverID)
			c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be greater than zero"})
			return
		default:
			log.Printf("admin_deduct_balance: unsupported amount type driver_id=%d type=%T", driverID, v)
			c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be a valid integer"})
			return
		}
		req.Reason = j.Reason
	} else {
		if err := c.ShouldBind(&req); err != nil {
			log.Printf("admin_deduct_balance: invalid non-JSON body driver_id=%d err=%v", driverID, err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	req.Reason = strings.TrimSpace(req.Reason)
	if req.Amount <= 0 {
		log.Printf("admin_deduct_balance: non-positive amount driver_id=%d amount=%d reason=%q", driverID, req.Amount, req.Reason)
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be greater than zero"})
		return
	}
	if req.Reason == "" {
		log.Printf("admin_deduct_balance: empty reason driver_id=%d amount=%d", driverID, req.Amount)
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason must not be empty"})
		return
	}

	ctx := c.Request.Context()
	promo, cash, total, isActive, deducted, wasCapped, err := h.svc.DeductDriverCashBalance(ctx, driverID, req.Amount, req.Reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("admin_deduct_balance: driver not found driver_id=%d amount=%d reason=%q", driverID, req.Amount, req.Reason)
			c.JSON(http.StatusNotFound, gin.H{"error": "driver not found"})
			return
		}
		log.Printf("admin_deduct_balance: service error driver_id=%d amount=%d reason=%q err=%v", driverID, req.Amount, req.Reason, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Notify driver via Telegram after successful deduction (best-effort; does not affect response).
	if h.driverBot != nil && deducted > 0 && h.db != nil {
		var telegramID int64
		if err := h.db.QueryRowContext(ctx, `SELECT u.telegram_id FROM users u JOIN drivers d ON d.user_id = u.id WHERE d.user_id = ?1`, driverID).Scan(&telegramID); err != nil {
			log.Printf("admin_deduct_balance: driver telegram lookup failed driver_id=%d err=%v", driverID, err)
		} else if telegramID != 0 {
			text := fmt.Sprintf("Балансингиздан %d сўм ечилди.\nСабаб: %s.\n\nЖорий нақд баланс: %d сўм.", deducted, req.Reason, cash)
			msg := tgbotapi.NewMessage(telegramID, text)
			if _, err := h.driverBot.Send(msg); err != nil {
				log.Printf("admin_deduct_balance: telegram notify failed driver_id=%d telegram_id=%d err=%v", driverID, telegramID, err)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success":           true,
		"driver_id":         driverID,
		"requested_amount":  req.Amount,
		"deducted_amount":   deducted,
		"promo_balance":     promo,
		"cash_balance":      cash,
		"balance":           total,
		"is_active":         isActive,
		"ledger_entry_type": "MANUAL_DEDUCTION",
		"reason":            req.Reason,
		"was_capped":        wasCapped,
	})
}

// ListPayments returns payment history, optionally filtered by driver_id query param.
func (h *AdminHandlers) ListPayments(c *gin.Context) {
	var driverIDPtr *int64
	if s := c.Query("driver_id"); s != "" {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
			driverIDPtr = &id
		}
	}
	payments, err := h.svc.ListPayments(c.Request.Context(), driverIDPtr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list payments"})
		return
	}
	c.JSON(http.StatusOK, payments)
}

// Dashboard returns aggregated admin dashboard statistics.
func (h *AdminHandlers) Dashboard(c *gin.Context) {
	summary, err := h.svc.GetDashboard(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load dashboard"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

