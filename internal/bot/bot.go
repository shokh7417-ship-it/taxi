package bot

import (
	"context"
	"database/sql"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/bot/admin"
	"taxi-mvp/internal/bot/driver"
	"taxi-mvp/internal/bot/rider"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/services"
)

// RunRiderBot runs the rider bot until ctx is cancelled.
func RunRiderBot(ctx context.Context, cfg *config.Config, db *sql.DB, riderBot *tgbotapi.BotAPI, matchService *services.MatchService, tripService *services.TripService) {
	if err := rider.Run(ctx, cfg, db, riderBot, matchService, tripService); err != nil {
		log.Printf("rider bot: %v", err)
	}
	log.Println("rider bot: stopped")
}

// RunDriverBot runs the driver bot until ctx is cancelled.
func RunDriverBot(ctx context.Context, cfg *config.Config, db *sql.DB, driverBot *tgbotapi.BotAPI, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService) {
	if err := driver.Run(ctx, cfg, db, driverBot, matchService, assignmentService, tripService); err != nil {
		log.Printf("driver bot: %v", err)
	}
	log.Println("driver bot: stopped")
}

// RunAdminBot runs the admin bot until ctx is cancelled. driverBot is used to notify drivers (they have no chat with admin bot).
func RunAdminBot(ctx context.Context, cfg *config.Config, db *sql.DB, adminBot *tgbotapi.BotAPI, fareSvc *services.FareService, driverBot *tgbotapi.BotAPI) {
	if adminBot == nil || fareSvc == nil || cfg == nil || cfg.AdminID == 0 {
		return
	}
	if err := admin.Run(ctx, cfg, db, adminBot, fareSvc, driverBot); err != nil {
		log.Printf("admin bot: %v", err)
	}
	log.Println("admin bot: stopped")
}
