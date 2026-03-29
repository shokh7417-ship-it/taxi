package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/bot"
	driverbot "taxi-mvp/internal/bot/driver"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/db"
	"taxi-mvp/internal/db/legalrepair"
	"taxi-mvp/internal/server"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/ws"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close(database)
	if err := legalrepair.Ensure(context.Background(), database); err != nil {
		log.Fatalf("legal schema repair: %v", err)
	}

	riderBot, err := tgbotapi.NewBotAPI(cfg.RiderBotToken)
	if err != nil {
		log.Fatalf("rider bot: %v", err)
	}
	driverBot, err := tgbotapi.NewBotAPI(cfg.DriverBotToken)
	if err != nil {
		log.Fatalf("driver bot: %v", err)
	}
	var adminBot *tgbotapi.BotAPI
	if cfg.AdminBotToken != "" {
		adminBot, err = tgbotapi.NewBotAPI(cfg.AdminBotToken)
		if err != nil {
			log.Printf("admin bot: %v (continuing without admin bot)", err)
			adminBot = nil
		}
	}

	matchSvc := services.NewMatchService(database, driverBot, cfg)
	assignSvc := services.NewAssignmentService(database, riderBot, driverBot, cfg)
	hub := ws.NewHub()
	go hub.Run()
	tripRepo := repositories.NewTripRepo(database)
	paymentRepo := repositories.NewPaymentRepository(database)
	fareSvc := services.NewFareService(database, cfg)
	tripSvc := services.NewTripService(database, tripRepo, riderBot, driverBot, cfg, hub, fareSvc, paymentRepo)
	tripSvc.OnDriverStatusUpdate = func(telegramID int64) {
		driverbot.UpdatePinnedStatusForChat(driverBot, database, cfg, telegramID)
	}

	// On each process start (deployment/restart), send a one-time notification to all drivers
	// so they know the system was updated and should go online again.
	go notifyDriversOfDeployment(database, driverBot)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutdown signal received")
		cancel()
	}()

	go assignSvc.RunExpiryWorker(ctx)
	go assignSvc.RunRadiusExpansionWorker(ctx, matchSvc)
	go services.RunOnlineBonusWorker(ctx, database, driverBot)
	go services.RunDriverApprovalNotifier(ctx, database, driverBot)
	go driverbot.RunLegalReacceptNotifier(ctx, database, driverBot)

	srv := server.New(database, cfg, tripSvc, matchSvc, driverBot, riderBot, hub, fareSvc)
	httpServer := &http.Server{Addr: cfg.APIAddr, Handler: srv}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("api server: %v", err)
		}
	}()

	// Delay bot startup so a previous process (e.g. old deploy) can release getUpdates and avoid "Conflict: terminated by other getUpdates request".
	const botStartDelay = 8 * time.Second
	select {
	case <-time.After(botStartDelay):
		log.Printf("bot startup delay done, starting Telegram bots")
	case <-ctx.Done():
		log.Println("shutdown during bot delay")
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		bot.RunRiderBot(ctx, cfg, database, riderBot, matchSvc, tripSvc)
	}()
	go func() {
		defer wg.Done()
		bot.RunDriverBot(ctx, cfg, database, driverBot, matchSvc, assignSvc, tripSvc)
	}()
	if adminBot != nil && cfg.AdminID != 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bot.RunAdminBot(ctx, cfg, database, adminBot, fareSvc, driverBot)
		}()
	}

	wg.Wait()
	_ = httpServer.Shutdown(context.Background())
	log.Println("graceful shutdown complete")
}

// notifyDriversOfDeployment sets all drivers offline, syncs balance<=0 to offline, then notifies all drivers (e.g. after deployment).
func notifyDriversOfDeployment(dbConn *sql.DB, driverBot *tgbotapi.BotAPI) {
	// Set all drivers offline so they must go online again after deploy.
	if _, err := dbConn.Exec(`UPDATE drivers SET is_active = 0`); err != nil {
		log.Printf("startup_notify: set drivers offline: %v", err)
	}
	// Ensure any driver with balance <= 0 is offline.
	if _, err := dbConn.Exec(`UPDATE drivers SET is_active = 0 WHERE COALESCE(balance, 0) <= 0`); err != nil {
		log.Printf("startup_notify: sync balance<=0 offline: %v", err)
	}
	if driverBot == nil {
		return
	}
	const text = "Tizim yangilandi. Buyurtmalar olish uchun jonli lokatsiyani qayta ulang (ulanganda avtomatik onlayn bo‘lasiz)."
	rows, err := dbConn.Query(`
		SELECT u.telegram_id
		FROM users u
		JOIN drivers d ON d.user_id = u.id
		WHERE u.telegram_id IS NOT NULL AND u.telegram_id != 0`)
	if err != nil {
		log.Printf("startup_notify: load drivers: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var telegramID int64
		if err := rows.Scan(&telegramID); err != nil {
			continue
		}
		if telegramID == 0 {
			continue
		}
		msg := tgbotapi.NewMessage(telegramID, text)
		msg.ReplyMarkup = driverbot.KeyboardForOffline()
		if _, err := driverBot.Send(msg); err != nil {
			log.Printf("startup_notify: send to %d: %v", telegramID, err)
		}
	}
}
