package driver

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/legal"
)

// sendDriverAgreementForDriver sends the latest active oferta from the DB when the driver is not fully compliant.
// periodicNudgeOnly: if true, when we already prompted this fingerprint and the driver still has not accepted, send only a short
// reminder (used by RunLegalReacceptNotifier). If false (/start, live block, etc.), always send full oferta text so the chat always has the contract.
func sendDriverAgreementForDriver(bot *tgbotapi.BotAPI, db *sql.DB, chatID, userID int64, alwaysFull, periodicNudgeOnly bool) {
	if bot == nil {
		return
	}
	ctx := context.Background()
	lSvc := legal.NewService(db)
	if lSvc.DriverHasActiveLegal(ctx, userID) {
		fp, err := legal.ActiveLegalFingerprint(ctx, db)
		if err != nil {
			log.Printf("driver: ActiveLegalFingerprint (sync) user_id=%d: %v", userID, err)
			return
		}
		var stored sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT legal_terms_prompt_fingerprint FROM drivers WHERE user_id = ?1`, userID).Scan(&stored); err != nil && err != sql.ErrNoRows {
			log.Printf("driver: read legal_terms_prompt_fingerprint (sync) user_id=%d: %v", userID, err)
			stored = sql.NullString{}
		}
		st := ""
		if stored.Valid {
			st = strings.TrimSpace(stored.String)
		}
		if st != fp {
			if _, err := db.ExecContext(ctx, `UPDATE drivers SET legal_terms_prompt_fingerprint = ?1 WHERE user_id = ?2`, fp, userID); err != nil {
				log.Printf("driver: update legal_terms_prompt_fingerprint user_id=%d: %v", userID, err)
			}
		}
		return
	}
	fp, err := legal.ActiveLegalFingerprint(ctx, db)
	if err != nil {
		log.Printf("driver: ActiveLegalFingerprint user_id=%d: %v", userID, err)
		sendDriverAgreement(bot, db, chatID)
		return
	}
	if fp == "" {
		sendDriverAgreement(bot, db, chatID)
		return
	}
	var stored sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT legal_terms_prompt_fingerprint FROM drivers WHERE user_id = ?1`, userID).Scan(&stored); err != nil && err != sql.ErrNoRows {
		log.Printf("driver: read legal_terms_prompt_fingerprint user_id=%d: %v", userID, err)
		stored = sql.NullString{}
	}
	st := ""
	if stored.Valid {
		st = strings.TrimSpace(stored.String)
	}
	if periodicNudgeOnly && !alwaysFull && st == fp {
		m := tgbotapi.NewMessage(chatID, "⚠️ Yangilangan shartnomani qabul qilish uchun «✅ Qabul qilaman» tugmasini bosing.")
		m.ReplyMarkup = driverAgreementInlineKeyboard()
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: legal nudge: %v", err)
		}
		return
	}
	if alwaysFull {
		send(bot, chatID, "📄 Haydovchi shartnomasi (oferta). Quyidagi xabar(lar)ni o‘qing va oxiridagi «Qabul qilaman» tugmasini bosing.")
	} else if st != "" && st != fp {
		labels := legal.ActiveLegalFingerprintLabels(fp)
		if labels != "" {
			send(bot, chatID, "📄 Shartnoma yangilandi. Faol versiyalar: "+labels+"\n\nQuyidagi matnni o‘qing va «✅ Qabul qilaman» tugmasini bosing.")
		} else {
			send(bot, chatID, "📄 Shartnoma yangilandi (yangi versiya). Quyidagi matnni o‘qing va qabul qiling.")
		}
	}
	sendDriverAgreement(bot, db, chatID)
	if _, err := db.ExecContext(ctx, `UPDATE drivers SET legal_terms_prompt_fingerprint = ?1 WHERE user_id = ?2`, fp, userID); err != nil {
		log.Printf("driver: update legal_terms_prompt_fingerprint user_id=%d: %v", userID, err)
	}
}

// RunLegalReacceptNotifier periodically nudges pending_approval and approved drivers who are not on the current active legal bundle.
func RunLegalReacceptNotifier(ctx context.Context, db *sql.DB, bot *tgbotapi.BotAPI) {
	if bot == nil {
		return
	}
	runLegalReacceptTick(ctx, db, bot)
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runLegalReacceptTick(ctx, db, bot)
		}
	}
}

func runLegalReacceptTick(ctx context.Context, db *sql.DB, bot *tgbotapi.BotAPI) {
	rows, err := db.QueryContext(ctx, `
		SELECT d.user_id, u.telegram_id
		FROM drivers d
		INNER JOIN users u ON u.id = d.user_id
		WHERE d.verification_status IN ('pending_approval', 'approved')
		  AND u.telegram_id IS NOT NULL AND u.telegram_id != 0`)
	if err != nil {
		log.Printf("driver legal reaccept notifier: query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var uid, tg int64
		if err := rows.Scan(&uid, &tg); err != nil {
			continue
		}
		sendDriverAgreementForDriver(bot, db, tg, uid, false, true)
	}
}
