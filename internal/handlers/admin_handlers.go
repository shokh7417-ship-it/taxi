package handlers

import (
	"log"
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
}

// NewAdminHandlers creates AdminHandlers. driverBot can be nil; then verify notifications are skipped.
func NewAdminHandlers(svc *services.AdminService, driverBot *tgbotapi.BotAPI) *AdminHandlers {
	return &AdminHandlers{svc: svc, driverBot: driverBot}
}

// Register registers /admin routes on the given router.
func (h *AdminHandlers) Register(r *gin.Engine) {
	if h == nil || h.svc == nil {
		return
	}
	g := r.Group("/admin")
	{
		g.GET("/drivers", h.ListDrivers)
		g.POST("/drivers/:id/add-balance", h.AddBalance)
		g.POST("/drivers/:id/verify", h.VerifyDriver)
		g.GET("/payments", h.ListPayments)
		g.GET("/dashboard", h.Dashboard)
	}
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update verification"})
		return
	}
	if h.driverBot != nil && telegramID != 0 {
		var text string
		if req.Status == "approved" {
			text = "🎉 Profilingiz tasdiqlandi!\n\nEndi siz buyurtmalar qabul qilishingiz mumkin.\n\nBoshlash uchun:\n\n🟢 Ishni boshlash\n📡 Jonli lokatsiyani yoqing"
		} else {
			text = "❗ Hujjatlar tasdiqlanmadi.\n\nIltimos, aniqroq rasm yuboring."
		}
		if _, err := h.driverBot.Send(tgbotapi.NewMessage(telegramID, text)); err != nil {
			log.Printf("admin: notify driver %d verification %s: %v", driverID, req.Status, err)
		}
	}
	c.Status(http.StatusNoContent)
}

// AddBalance performs a manual deposit/top-up to driver balance.
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

