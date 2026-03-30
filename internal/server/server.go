package server

import (
	"database/sql"
	"log"
	"time"

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
	r := gin.New()
	// Avoid gin's default access logger which may include full query strings
	// (e.g. Telegram init_data on websocket requests), causing large stdout/stderr.
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())
	r.Use(func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		status := c.Writer.Status()
		// Keep logs small: do not log query strings or request bodies.
		log.Printf("http_request method=%s path=%s status=%d dur_ms=%d", c.Request.Method, path, status, time.Since(start).Milliseconds())
	})

	healthHandler := func(c *gin.Context) {
		// Keep health response extremely small and constant (external monitors rely on this).
		// Do not touch DB, logs, or external services.
		c.Data(200, "application/json; charset=utf-8", []byte(`{"status":"ok"}`))
	}
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
	r.POST("/trip/arrived", tryDriverID, driverAuth, handlers.TripArrived(db, tripSvc))
	r.POST("/trip/finish", tryDriverID, driverAuth, handlers.TripFinish(db, tripSvc))
	r.POST("/trip/cancel/driver", tryDriverID, driverAuth, handlers.TripCancelDriver(db, tripSvc))
	r.GET("/driver/referral-link", tryDriverID, driverAuth, handlers.DriverReferralLink(db, driverBot))
	r.GET("/driver/promo-program", tryDriverID, driverAuth, handlers.DriverPromoProgram(db))
	r.GET("/driver/referral-status", tryDriverID, driverAuth, handlers.DriverReferralStatus(db))
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
	// Legal admin API for dashboards (always mount when DB is available; do not depend on handler wiring).
	handlers.RegisterAdminLegalRoutes(r, db)
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
