package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/services"
)

// AdminHandlers exposes admin HTTP endpoints.
type AdminHandlers struct {
	svc       *services.AdminService
	driverBot *tgbotapi.BotAPI
	db        *sql.DB
}

// NewAdminHandlers creates AdminHandlers. driverBot can be nil; then verify notifications are skipped.
// db is used for legal monitoring and rider list routes; may be nil (those routes are skipped).
func NewAdminHandlers(svc *services.AdminService, driverBot *tgbotapi.BotAPI, db *sql.DB) *AdminHandlers {
	return &AdminHandlers{svc: svc, driverBot: driverBot, db: db}
}

// Register registers /admin routes on the given router.
func (h *AdminHandlers) Register(r *gin.Engine) {
	if h == nil || h.svc == nil {
		return
	}
	g := r.Group("/admin")
	{
		g.GET("/drivers", h.ListDrivers)
		g.GET("/drivers/:id/ledger", h.ListDriverLedger)
		g.GET("/riders", h.ListRiders)
		g.POST("/drivers/:id/add-balance", h.AddBalance)
		g.POST("/drivers/:id/adjust-balance", h.AdjustBalance)
		g.POST("/drivers/:id/verify", h.VerifyDriver)
		g.GET("/payments", h.ListPayments)
		g.GET("/dashboard", h.Dashboard)
	}
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

type addBalanceRequest struct {
	Amount int64  `json:"amount"` // in smallest currency units (e.g. so'm)
	Note   string `json:"note"`
}

type adjustBalanceRequest struct {
	Amount  int64  `json:"amount"`   // signed delta; positive = credit, negative = debit
	Reason  string `json:"reason"`   // human-readable reason; stored in audit log
	AdminID int64  `json:"admin_id"` // admin user id for audit metadata
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
	if _, err := h.svc.SetDriverVerification(c.Request.Context(), driverID, req.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update verification"})
		return
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
		"success":            true,
		"driver_id":          driverID,
		"delta":              req.Amount,
		"promo_balance":      promo,
		"cash_balance":       cash,
		"balance":            total,
		"is_active":          isActive,
		"ledger_entry_type":  "MANUAL_ADJUSTMENT",
		"reason":             req.Reason,
		"admin_id":           req.AdminID,
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

