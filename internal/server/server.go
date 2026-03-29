package server

import (
	"database/sql"

	"github.com/gin-gonic/gin"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/handlers"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/ws"
)

// New creates a Gin engine with API routes and optional webapp static files.
// hub can be nil; if set, GET /ws is registered. fareSvc can be nil (then fare uses config only).
// matchSvc and driverBot are used for driver auto-availability and notifications (e.g. after trip finish + Mini App location).
// riderBot is optional; used for rider referral link (bot username).
func New(db *sql.DB, cfg *config.Config, tripSvc *services.TripService, matchSvc *services.MatchService, driverBot *tgbotapi.BotAPI, riderBot *tgbotapi.BotAPI, hub *ws.Hub, fareSvc *services.FareService) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Use(corsMiddleware())

	healthHandler := func(c *gin.Context) { c.String(200, "ok") }
	r.GET("/health", healthHandler)
	r.HEAD("/health", healthHandler)
	r.GET("/", healthHandler)
	r.HEAD("/", healthHandler)

	tryDriverID := auth.TryDriverIDHeader(db)
	driverAuth := auth.RequireDriverAuth(db, cfg.DriverBotToken, cfg.EnableDriverIDHeader)
	riderAuth := auth.RequireRiderAuth(db, cfg.RiderBotToken)
	appUserAuth := auth.RequireMiniAppAuthDriverOrRider(db, cfg.DriverBotToken, cfg.RiderBotToken)

	if hub != nil {
		r.GET("/ws", func(c *gin.Context) {
			ws.ServeWsWithAuth(hub, db, cfg.DriverBotToken, cfg.RiderBotToken, c.Writer, c.Request)
		})
	}

	r.GET("/trip/:id", handlers.TripInfo(db, cfg, fareSvc))
	// Mini App: try X-Driver-Id first so Start/Cancel/Finish work without initData when header is present
	r.POST("/driver/location", tryDriverID, driverAuth, handlers.DriverLocation(db, tripSvc, matchSvc, driverBot, hub, cfg, fareSvc))
	r.POST("/trip/start", tryDriverID, driverAuth, handlers.TripStart(db, tripSvc))
	r.POST("/trip/finish", tryDriverID, driverAuth, handlers.TripFinish(db, tripSvc))
	r.POST("/trip/cancel/driver", tryDriverID, driverAuth, handlers.TripCancelDriver(db, tripSvc))
	r.GET("/driver/referral-link", tryDriverID, driverAuth, handlers.DriverReferralLink(db, driverBot))
	r.POST("/trip/cancel/rider", riderAuth, handlers.TripCancelRider(db, tripSvc))
	r.GET("/rider/referral-link", riderAuth, handlers.RiderReferralLink(db, riderBot))

	// Legal: active documents + accept (active versions only; X-Driver-Id allowed when enabled).
	r.GET("/legal/active", tryDriverID, appUserAuth, handlers.LegalActiveDocuments(db))
	r.POST("/legal/accept", tryDriverID, appUserAuth, handlers.LegalAccept(db))

	r.Static("/webapp", "./webapp")

	// Admin HTTP API (dashboard, drivers, payments, driver verification). Additive; does not change trip/dispatch/location logic.
	adminDriverRepo := repositories.NewAdminDriverRepository(db)
	paymentRepo := repositories.NewPaymentRepository(db)
	tripStatsRepo := repositories.NewTripStatsRepository(db)
	adminSvc := services.NewAdminService(db, adminDriverRepo, paymentRepo, tripStatsRepo)
	adminHandlers := handlers.NewAdminHandlers(adminSvc, driverBot, db)
	adminHandlers.Register(r)
	return r
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Telegram-Init-Data, X-Driver-Id")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
