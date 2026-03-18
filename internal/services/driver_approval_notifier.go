package services

import (
	"context"
	"database/sql"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/legal"
)

// RunDriverApprovalNotifier periodically checks for drivers whose verification_status is 'approved'
// but approval_notified = 0, sends them approval and bonus info via the driver bot, and marks them notified.
func RunDriverApprovalNotifier(ctx context.Context, db *sql.DB, driverBot *tgbotapi.BotAPI) {
	if driverBot == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notifyApprovedDrivers(ctx, db, driverBot)
		}
	}
}

func notifyApprovedDrivers(ctx context.Context, db *sql.DB, driverBot *tgbotapi.BotAPI) {
	rows, err := db.QueryContext(ctx, `
		SELECT u.telegram_id, d.user_id
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE COALESCE(d.verification_status, '') = 'approved'
		  AND COALESCE(d.approval_notified, 0) = 0`)
	if err != nil {
		log.Printf("driver_approval_notifier: query: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var telegramID int64
		var userID int64
		if err := rows.Scan(&telegramID, &userID); err != nil || telegramID == 0 {
			continue
		}

		// 1) Profil tasdiqlandi xabari
		msg := tgbotapi.NewMessage(telegramID, "🎉 Profilingiz tasdiqlandi!\n\nEndi siz buyurtmalar qabul qilishingiz mumkin.\n\n🟢 Ishni boshlash\n📡 Jonli lokatsiyani yoqing\n\nVideo qo'llanmalar va yangiliklar shu yerda!!!\nhttps://t.me/+iD_MYyWnntE1NmMy")
		if _, err := driverBot.Send(msg); err != nil {
			log.Printf("driver_approval_notifier: send approved message user_id=%d: %v", userID, err)
			continue
		}

		// Show short Terms of Use once per user (anti-spam via users.terms_accepted flag).
		showTermsShortOnceForUser(ctx, db, driverBot, telegramID)

		// 2) Bonuslar haqida xabar (agar hali yuborilmagan bo'lsa)
		var welcomeSent int
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(welcome_bonus_message_sent, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&welcomeSent); err == nil && welcomeSent == 0 {
			welcome := tgbotapi.NewMessage(telegramID, "🎁 Haydovchi bonuslari\n\n1️⃣ Yangi haydovchi bonusi: 100 000 so'm platform krediti (hisobingizga qo'shildi)\n\n2️⃣ Online bonus: 1 soat online → +2 000 so'm. Kunlik limit: 20 000 so'm")
			if _, err := driverBot.Send(welcome); err != nil {
				log.Printf("driver_approval_notifier: send welcome bonus message user_id=%d: %v", userID, err)
			} else {
				_, _ = db.ExecContext(ctx, `UPDATE drivers SET welcome_bonus_message_sent = 1 WHERE user_id = ?1`, userID)
			}
		}

		// 3) Reply keyboard with "Ishni boshlash" and "Jonli lokatsiya yoqish" so driver sees the buttons.
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButton("🟢 Ishni boshlash"),
				tgbotapi.NewKeyboardButton("📡 Jonli lokatsiya yoqish"),
			),
		)
		kb.ResizeKeyboard = true
		keyboardMsg := tgbotapi.NewMessage(telegramID, "Quyidagi tugmalardan foydalaning:")
		keyboardMsg.ReplyMarkup = kb
		if _, err := driverBot.Send(keyboardMsg); err != nil {
			log.Printf("driver_approval_notifier: send keyboard user_id=%d: %v", userID, err)
		}

		// Mark as notified so we don't resend.
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET approval_notified = 1 WHERE user_id = ?1`, userID)
	}
	if err := rows.Err(); err != nil {
		log.Printf("driver_approval_notifier: rows: %v", err)
	}
}

func showTermsShortOnceForUser(ctx context.Context, db *sql.DB, bot *tgbotapi.BotAPI, telegramID int64) {
	var accepted int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(terms_accepted, 0) FROM users WHERE telegram_id = ?1`, telegramID).Scan(&accepted); err != nil {
		return
	}
	if accepted != 0 {
		return
	}
	if _, err := bot.Send(tgbotapi.NewMessage(telegramID, legal.TermsShortMessage)); err != nil {
		return
	}
	_, _ = db.ExecContext(ctx, `UPDATE users SET terms_accepted = 1 WHERE telegram_id = ?1`, telegramID)
}

