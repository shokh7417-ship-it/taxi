package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	onlineBonusSoMPerHour    = 2000
	onlineBonusMaxSoMPerDay  = 20000
	onlineBonusLiveWithinSec = 60
	onlineBonusTickInterval  = 2 * time.Minute
)

// RunOnlineBonusWorker runs a loop that accrues online time bonus for eligible drivers every tick.
// Eligible: is_active=1, live_location_active=1, last_live_location_at within 60 seconds.
// Credits 2000 so'm per hour, max 20000 so'm per day; resets at midnight. Sends driver messages when earning or when daily limit reached.
func RunOnlineBonusWorker(ctx context.Context, db *sql.DB, driverBot *tgbotapi.BotAPI) {
	ticker := time.NewTicker(onlineBonusTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnlineBonusAccrual(ctx, db, driverBot)
		}
	}
}

func runOnlineBonusAccrual(ctx context.Context, db *sql.DB, driverBot *tgbotapi.BotAPI) {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	cutoff := now.Add(-time.Duration(onlineBonusLiveWithinSec) * time.Second).Format("2006-01-02 15:04:05")

	rows, err := db.QueryContext(ctx, `
		SELECT d.user_id, u.telegram_id,
		       COALESCE(d.balance, 0), COALESCE(d.online_bonus_so_m_today, 0), d.online_bonus_last_credited_at, COALESCE(d.online_bonus_last_day, '')
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE COALESCE(d.is_active, 0) = 1
		  AND COALESCE(d.live_location_active, 0) = 1
		  AND d.last_live_location_at IS NOT NULL
		  AND d.last_live_location_at >= ?1`, cutoff)
	if err != nil {
		log.Printf("online_bonus: query: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var userID int64
		var telegramID int64
		var balance, earnedToday int64
		var lastCreditedAt, lastDay sql.NullString
		if err := rows.Scan(&userID, &telegramID, &balance, &earnedToday, &lastCreditedAt, &lastDay); err != nil {
			continue
		}

		// Reset at midnight: new day
		if lastDay.String != today {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET online_bonus_so_m_today = 0, online_bonus_last_day = ?1 WHERE user_id = ?2`, today, userID)
			earnedToday = 0
			lastCreditedAt = sql.NullString{}
		}

		// Elapsed since last credit
		var fromTime time.Time
		if lastCreditedAt.Valid && lastCreditedAt.String != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", lastCreditedAt.String, time.UTC); err == nil {
				fromTime = t
			} else {
				fromTime = now.Add(-onlineBonusTickInterval)
			}
		} else {
			fromTime = now.Add(-onlineBonusTickInterval)
		}
		elapsed := now.Sub(fromTime)
		if elapsed <= 0 {
			continue
		}

		// 2000 so'm per hour, credited in whole 2000 so'm steps only.
		raw := int64(onlineBonusSoMPerHour * elapsed.Seconds() / 3600)
		toAdd := (raw / onlineBonusSoMPerHour) * onlineBonusSoMPerHour
		if toAdd <= 0 {
			continue
		}
		remaining := onlineBonusMaxSoMPerDay - earnedToday
		if remaining <= 0 {
			continue
		}
		if toAdd > remaining {
			toAdd = remaining
		}

		nowStr := now.Format("2006-01-02 15:04:05")
		res, err := db.ExecContext(ctx, `
			UPDATE drivers
			SET balance = balance + ?1,
			    online_bonus_so_m_today = online_bonus_so_m_today + ?1,
			    online_bonus_last_credited_at = ?2,
			    online_bonus_last_day = ?3
			WHERE user_id = ?4`,
			toAdd, nowStr, today, userID)
		if err != nil {
			log.Printf("online_bonus: update driver %d: %v", userID, err)
			continue
		}
		if nr, _ := res.RowsAffected(); nr == 0 {
			continue
		}

		newEarnedToday := earnedToday + toAdd

		if driverBot != nil && telegramID != 0 {
			if newEarnedToday >= onlineBonusMaxSoMPerDay {
				msg := tgbotapi.NewMessage(telegramID, "🎉 Bugungi online bonus limiti tugadi.\n\nErtaga yana bonus ishlaydi.")
				if _, err := driverBot.Send(msg); err != nil {
					log.Printf("online_bonus: send limit message: %v", err)
				}
			} else if toAdd >= onlineBonusSoMPerHour || earnedToday == 0 {
				// Every hour while online: progress message.
				msg := tgbotapi.NewMessage(telegramID, fmt.Sprintf("💰 Online bonus\n\n+2 000 so'm qo'shildi\n\nBugun ishlagan bonus:\n%d / 20 000 so'm", newEarnedToday))
				if _, err := driverBot.Send(msg); err != nil {
					log.Printf("online_bonus: send bonus message: %v", err)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("online_bonus: rows: %v", err)
	}
}

