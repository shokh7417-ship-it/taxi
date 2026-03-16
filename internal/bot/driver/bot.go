package driver

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

const (
	btnOnline       = "🟢 Onlinega o'tish"
	btnStartWork    = "🟢 Ishni boshlash"
	btnStopWork     = "🔴 Ishni to'xtatish"
	btnOffline      = "🔴 Offlinega o'tish"
	btnLiveLocation = "📡 Jonli lokatsiya yoqish"
	btnPending      = "⏳ Tasdiqlash kutilmoqda"
	cbAccept        = "accept:"

	// Live Location = only edited_message.location updates; active only when last_live_location_at within 90s.
	liveLocationActiveSeconds = 90
	// Onboarding: shown when driver completes registration.
	onboardingMessage = "🚕 YettiQanot Haydovchi\n\nBuyurtmalar olish uchun 2 ta qadam:\n\n1️⃣ Ishni boshlash (online bo'lish)\n2️⃣ Jonli lokatsiyani yoqish\n\nShundan keyin sizga yaqin buyurtmalar keladi."

	// Welcome bonus message: explains all driver bonuses (shown once after registration).
	welcomeBonusMessage = "🎁 Haydovchi bonuslari\n\n1️⃣ Yangi haydovchi bonusi: 100 000 so'm platform krediti (hisobingizga qo'shildi)\n\n2️⃣ Birinchi oy 0% komissiya\n\n3️⃣ Online bonus: 1 soat online → +2 000 so'm. Kunlik limit: 20 000 so'm"
	// Bilingual instruction line for all Live Location prompts.
	liveLocationBilingualInstruction = "📎 → Геопозиция / Location → Транслировать геопозицию / Share Live Location"
	// Instruction when driver presses "Jonli lokatsiya yoqish" and is not sharing live.
	liveLocationInstructionMessage = "📍 Jonli lokatsiyani yoqsangiz, yaqin buyurtmalar sizga tezroq keladi.\n\n" + liveLocationBilingualInstruction
	// Online but no Live: full reminder with numbered steps (shown once per 8h to avoid spam).
	onlineNoLiveReminderMessage = "📡 Siz onlinesiz, lekin jonli lokatsiya yoqilmagan.\n\nBuyurtmalar olish uchun jonli lokatsiyani yoqing.\n\nQanday yoqiladi:\n\n1️⃣ Pastdagi 📎 tugmasini bosing\n2️⃣ Геопозиция / Location ni tanlang\n3️⃣ Транслировать геопозицию / Share Live Location ni bosing\n4️⃣ 8 soat (8 hours) ni tanlang — tavsiya qilinadi"
	// Short message when online but no live (within cooldown): accurate, never claim "So'rovlar keladi".
	onlineNoLiveShortMessage = "🟢 Siz onlinesiz.\n\nBuyurtmalar olish uchun jonli lokatsiyani yoqing.\n\n📎 → Геопозиция / Location → Транслировать геопозицию / Share Live Location"
	// When Live Location becomes active (once).
	liveLocationConfirmMessage = "📡 Jonli lokatsiya qabul qilindi.\nEndi sizga yaqin buyurtmalar keladi."
	// One-time warning when Live Location becomes inactive.
	liveLocationInactiveWarningMessage = "📍 Jonli lokatsiya o'chdi.\n\nBuyurtmalar kelmaydi.\n\nQayta yoqish uchun:\n\n📎 → Геопозиция / Location →\nТранслировать геопозицию / Share Live Location"
	// When driver goes offline.
	offlineMessage = "🔴 Siz oflaynsiz.\n\nBuyurtmalar kelmaydi."
	// Offline but sharing Live Location: remind to go online (once per cooldown).
	offlineButLiveReminderMessage = "📡 Jonli lokatsiya yoqilgan, lekin siz oflaynsiz.\n\nBuyurtmalar olish uchun onlinega o'ting."
	liveLocationHintCooldownHours   = 8
	insufficientBalanceMessage        = "Balansingiz yetarli emas. So'rovlar olish uchun balansni to'ldiring."
	staticLocationRejectionMessage    = "❌ Oddiy lokatsiya qabul qilinmaydi.\n\nBuyurtmalar olish uchun jonli lokatsiya ulashing.\n\n" + liveLocationBilingualInstruction

	// Registration: car types (Uzbekistan taxi market). "Boshqa" allows manual input.
	carTypeBoshqa = "Boshqa"
)
var (
	carTypes = []string{"Cobalt", "Nexia", "Nexia 2", "Nexia 3", "Matiz", "Gentra", "Lacetti", "Malibu", "BYD", "Lada", "Damas", carTypeBoshqa}
	colors   = []string{"Oq", "Qora", "Sariq", "Qizil", "Kulrang", "Boshqa"}
)

func carTypeKeyboard() tgbotapi.ReplyKeyboardMarkup {
	// Layout: Cobalt Nexia | Nexia 2 Nexia 3 | Matiz Gentra | Lacetti Malibu | BYD Lada | Damas Boshqa
	rows := [][]tgbotapi.KeyboardButton{
		{tgbotapi.NewKeyboardButton("Cobalt"), tgbotapi.NewKeyboardButton("Nexia")},
		{tgbotapi.NewKeyboardButton("Nexia 2"), tgbotapi.NewKeyboardButton("Nexia 3")},
		{tgbotapi.NewKeyboardButton("Matiz"), tgbotapi.NewKeyboardButton("Gentra")},
		{tgbotapi.NewKeyboardButton("Lacetti"), tgbotapi.NewKeyboardButton("Malibu")},
		{tgbotapi.NewKeyboardButton("BYD"), tgbotapi.NewKeyboardButton("Lada")},
		{tgbotapi.NewKeyboardButton("Damas"), tgbotapi.NewKeyboardButton(carTypeBoshqa)},
	}
	kb := tgbotapi.NewReplyKeyboard(rows...)
	kb.ResizeKeyboard = true
	return kb
}

func colorKeyboard() tgbotapi.ReplyKeyboardMarkup {
	rows := [][]tgbotapi.KeyboardButton{
		{tgbotapi.NewKeyboardButton("Oq"), tgbotapi.NewKeyboardButton("Qora")},
		{tgbotapi.NewKeyboardButton("Sariq"), tgbotapi.NewKeyboardButton("Qizil")},
		{tgbotapi.NewKeyboardButton("Kulrang"), tgbotapi.NewKeyboardButton("Boshqa")},
	}
	kb := tgbotapi.NewReplyKeyboard(rows...)
	kb.ResizeKeyboard = true
	return kb
}

func isDigitsOnly(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// getUserLang and alphabet selection were removed; bot now always uses Uzbek Latin.

// isDriverBalanceSufficient returns true if the driver is eligible for dispatch (balance > 0, or when InfiniteDriverBalance is true).
func isDriverBalanceSufficient(ctx context.Context, db *sql.DB, driverUserID int64, cfg *config.Config) bool {
	if cfg != nil && cfg.InfiniteDriverBalance {
		return true
	}
	var balance int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(balance, 0) FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&balance); err != nil {
		return false
	}
	return balance > 0
}

// isDriverFullyRegistered returns true if the driver has completed registration and can use location/dispatch.
// Either: (phone, car_type, color, plate + both doc photos) or (same four + verification_status = 'approved' for existing drivers).
func isDriverFullyRegistered(ctx context.Context, db *sql.DB, driverUserID int64) bool {
	var phone, carType, color, plate, licenseFileID, vehicleFileID, verificationStatus sql.NullString
	err := db.QueryRowContext(ctx, `SELECT phone, car_type, color, plate, license_photo_file_id, vehicle_doc_file_id, verification_status FROM drivers WHERE user_id = ?1`, driverUserID).
		Scan(&phone, &carType, &color, &plate, &licenseFileID, &vehicleFileID, &verificationStatus)
	if err != nil {
		return false
	}
	baseOk := phone.Valid && strings.TrimSpace(phone.String) != "" &&
		carType.Valid && strings.TrimSpace(carType.String) != "" &&
		color.Valid && strings.TrimSpace(color.String) != "" &&
		plate.Valid && strings.TrimSpace(plate.String) != ""
	if !baseOk {
		return false
	}
	hasDocs := licenseFileID.Valid && strings.TrimSpace(licenseFileID.String) != "" &&
		vehicleFileID.Valid && strings.TrimSpace(vehicleFileID.String) != ""
	approved := verificationStatus.Valid && strings.TrimSpace(verificationStatus.String) == "approved"
	return hasDocs || approved
}

// parseUTC parses a "2006-01-02 15:04:05" string as UTC (stored timestamps are UTC).
func parseUTC(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
}

// isDriverSharingLiveLocation returns true only when Telegram has sent a live update recently (edited_message.location).
// Uses only last_live_location_at; active iff now - last_live_location_at <= 90 seconds. Parses as UTC to match stored format.
func isDriverSharingLiveLocation(ctx context.Context, db *sql.DB, driverUserID int64) bool {
	var lastLive sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT last_live_location_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastLive)
	if !lastLive.Valid || lastLive.String == "" {
		return false
	}
	t, err := parseUTC(lastLive.String)
	if err != nil {
		return false
	}
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationActiveSeconds) * time.Second)
	return t.After(cutoff)
}

// shouldShowLiveLocationInstructionForStatic returns true if we should show the instruction after static location:
// driver is NOT sharing live and we have not sent the (static) hint in the last 8h.
func shouldShowLiveLocationInstructionForStatic(ctx context.Context, db *sql.DB, driverUserID int64) bool {
	if isDriverSharingLiveLocation(ctx, db, driverUserID) {
		return false
	}
	var lastHint sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT live_location_hint_last_sent_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastHint)
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationHintCooldownHours) * time.Hour)
	if lastHint.Valid && lastHint.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastHint.String); err == nil && t.After(cutoff) {
			return false
		}
	}
	return true
}

// shouldShowOnlineNoLiveReminder returns true if we have not sent the "online but no live" reminder in the last 8 hours (send once per 8h).
func shouldShowOnlineNoLiveReminder(ctx context.Context, db *sql.DB, driverUserID int64) bool {
	var lastHint sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT live_location_hint_last_sent_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastHint)
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationHintCooldownHours) * time.Hour)
	if lastHint.Valid && lastHint.String != "" {
		if t, err := parseUTC(lastHint.String); err == nil && t.After(cutoff) {
			return false
		}
	}
	return true
}

// shouldShowOnOnlineLiveLocationMessage returns true if we should show the long "on Online" hint:
// driver has not used live location in the last 8h and we have not sent the on-online hint in the last 8h.
// Uses a separate cooldown so the long message is sent when they tap Online even if the short hint was shown at /start.
func shouldShowOnOnlineLiveLocationMessage(ctx context.Context, db *sql.DB, driverUserID int64) bool {
	var lastLive, onOnlineHint sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT last_live_location_at, live_location_on_online_hint_last_sent_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastLive, &onOnlineHint)
	cutoff := time.Now().UTC().Add(-time.Duration(liveLocationHintCooldownHours) * time.Hour)
	if lastLive.Valid && lastLive.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastLive.String); err == nil && t.After(cutoff) {
			return false
		}
	}
	if onOnlineHint.Valid && onOnlineHint.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", onOnlineHint.String); err == nil && t.After(cutoff) {
			return false
		}
	}
	return true
}

const offlineLiveReminderCooldownMin = 60

// sendOfflineButLiveReminderIfNeeded sends a one-time reminder when driver is offline but sharing Live Location (cooldown 1 hour).
func sendOfflineButLiveReminderIfNeeded(bot *tgbotapi.BotAPI, db *sql.DB, chatID int64, driverUserID int64) {
	ctx := context.Background()
	var lastSent sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT live_location_offline_reminder_last_sent_at FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&lastSent); err != nil {
		return
	}
	if lastSent.Valid && lastSent.String != "" {
		if t, err := parseUTC(lastSent.String); err == nil && time.Since(t) < offlineLiveReminderCooldownMin*time.Minute {
			return
		}
	}
	kb := getDriverKeyboard(db, driverUserID)
	m := tgbotapi.NewMessage(chatID, offlineButLiveReminderMessage)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send offline-but-live reminder: %v", err)
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_offline_reminder_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, driverUserID)
}

// liveLocationButtonInstructionCooldownMin: do not resend the same instruction if sent this recently (button press spam).
const liveLocationButtonInstructionCooldownMin = 3

// handleLiveLocationInstruction runs when the driver presses "📡 Jonli lokatsiya yoqish". If already sharing live, ignore. Else send instruction once per cooldown to avoid spam.
func handleLiveLocationInstruction(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
		send(bot, chatID, "Xatolik.")
		return
	}
	var verificationStatus sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, userID).Scan(&verificationStatus)
	if !verificationStatus.Valid || strings.TrimSpace(verificationStatus.String) != "approved" {
		kb := driverKeyboardForVerificationPending()
		m := tgbotapi.NewMessage(chatID, "Tasdiqlash kutilmoqda. Admin profilingizni tekshirmoqda.")
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send pending verification live-location message: %v", err)
		}
		return
	}
	if isDriverSharingLiveLocation(ctx, db, userID) {
		return
	}
	var lastHint sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT live_location_hint_last_sent_at FROM drivers WHERE user_id = ?1`, userID).Scan(&lastHint)
	if lastHint.Valid && lastHint.String != "" {
		if t, err := parseUTC(lastHint.String); err == nil && time.Since(t) < liveLocationButtonInstructionCooldownMin*time.Minute {
			return
		}
	}
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, liveLocationInstructionMessage)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send live location instruction: %v", err)
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, userID)
}

// sendOnOnlineLiveLocationInstruction sends the instruction when driver goes Online, only if not sharing live (8h cooldown).
func sendOnOnlineLiveLocationInstruction(bot *tgbotapi.BotAPI, db *sql.DB, chatID, driverUserID int64) {
	ctx := context.Background()
	if !shouldShowOnOnlineLiveLocationMessage(ctx, db, driverUserID) {
		return
	}
	kb := getDriverKeyboard(db, driverUserID)
	m := tgbotapi.NewMessage(chatID, liveLocationInstructionMessage)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send on-online live location instruction: %v", err)
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_on_online_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, driverUserID)
}

// Run starts the driver bot and blocks until ctx is cancelled.
// bot is the driver Telegram bot API; matchService for dispatch and pending-request push; assignmentService for Accept; tripService for START/AddPoint/FINISH (may be nil).
func Run(ctx context.Context, cfg *config.Config, db *sql.DB, bot *tgbotapi.BotAPI, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService) error {
	log.Printf("driver bot: started @%s", bot.Self.UserName)

	// Set command panel (global, Latin descriptions):
		// /start, /status, /bonuslar, /referral, /leaderboard (online/offline via buttons only).
	driverCommands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Botni boshlash"},
		tgbotapi.BotCommand{Command: "status", Description: "Holat va balans"},
		tgbotapi.BotCommand{Command: "bonuslar", Description: "Bonuslar va referral statistikasi"},
		tgbotapi.BotCommand{Command: "referral", Description: "Do'stlarni taklif qilish"},
		tgbotapi.BotCommand{Command: "leaderboard", Description: "Eng faol haydovchilar"},
	)
	if _, err := bot.Request(driverCommands); err != nil {
		log.Printf("driver bot: setMyCommands: %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	u.AllowedUpdates = []string{"message", "edited_message", "callback_query"}
	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			handleUpdate(bot, db, cfg, matchService, assignmentService, tripService, update)
		}
	}
}

// driverKeyboardForStatus returns main control panel: offline [Ishni boshlash, Jonli lokatsiya yoqish], online [Ishni to'xtatish, Jonli lokatsiya yoqish].
func driverKeyboardForStatus(isOnline bool) tgbotapi.ReplyKeyboardMarkup {
	// Note: driverKeyboardForStatus is used when we don't know telegramID/lang;
	// buttons themselves are emojis + short text, kept in Latin to avoid DB lookups here.
	var row []tgbotapi.KeyboardButton
	if isOnline {
		row = append(row, tgbotapi.NewKeyboardButton(btnStopWork), tgbotapi.NewKeyboardButton(btnLiveLocation))
	} else {
		row = append(row, tgbotapi.NewKeyboardButton(btnStartWork), tgbotapi.NewKeyboardButton(btnLiveLocation))
	}
	kb := tgbotapi.NewReplyKeyboard(tgbotapi.NewKeyboardButtonRow(row...))
	kb.ResizeKeyboard = true
	return kb
}

// getDriverKeyboard returns the keyboard for the driver's current status (single row: Jonli + Online or Offline).
func getDriverKeyboard(db *sql.DB, driverUserID int64) tgbotapi.ReplyKeyboardMarkup {
	ctx := context.Background()
	var isActive int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(is_active, 0) FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&isActive)
	return driverKeyboardForStatus(isActive == 1)
}

// KeyboardForOffline returns the reply keyboard for offline state (e.g. after deployment), so the driver sees "Online" and can go online.
func KeyboardForOffline() tgbotapi.ReplyKeyboardMarkup {
	return driverKeyboardForStatus(false)
}

func driverKeyboardForVerificationPending() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnPending),
		),
	)
	kb.ResizeKeyboard = true
	return kb
}

// formatStatusPanelText returns the status panel text (same layout as /status and pinned panel), including today's online bonus.
// State mapping:
// 🔴 Offline                -> is_active = 0
// 🟢 Online (no live)       -> is_active = 1 and live location not recent
// 🟡 Online + Live Location -> is_active = 1 and live location recent (bonus-eligible)
func formatStatusPanelText(ctx context.Context, db *sql.DB, userID int64) (string, error) {
	var isActive int
	var balance int64
	var lastLiveAt sql.NullString
	var onlineBonusToday int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(is_active, 0), COALESCE(balance, 0), last_live_location_at, COALESCE(online_bonus_so_m_today, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&isActive, &balance, &lastLiveAt, &onlineBonusToday); err != nil {
		return "", err
	}
	liveLine := "❌ Jonli lokatsiya yoqilmagan"
	liveRecent := false
	if lastLiveAt.Valid && lastLiveAt.String != "" {
		if t, err := parseUTC(lastLiveAt.String); err == nil && time.Since(t) <= 60*time.Second {
			liveRecent = true
			liveLine = "📡 Jonli lokatsiya yoqilgan"
		}
	}

	// Online bo'lish = faqat bonusActive holat (online + jonli lokatsiya ON).
	// Aks holda holat offline hisoblanadi (offline yoki jonli lokatsiya o'chgan).
	holat := "🔴 Offline"
	if isActive == 1 && liveRecent {
		holat = "🟢 Online"
	}

	text := "📊 Haydovchi holati\n\n"
	text += fmt.Sprintf("Holat: %s\nLokatsiya: %s\n\n", holat, liveLine)

	switch {
	// A) online + location on (bonusActive)
	case isActive == 1 && liveRecent:
		text += "✅ Buyurtmalar olishga tayyor.\n💰 Bonus ishlayapti."

	// C) offline + location on (lokatsiya bor, lekin offline)
	case isActive == 0 && liveRecent:
		text += "⚠️ Buyurtma olish uchun online bo‘ling."

	// B / D) location off (offline deb ko‘rsatamiz)
	default:
		text += "⚠️ Ishlash uchun online bo‘ling va lokatsiyani yoqing."
	}

	return text, nil
}

// sendOrUpdatePinnedStatus sends or updates the pinned status message for the driver. If a pinned message exists, edits it; otherwise sends new, stores message_id, and pins.
func sendOrUpdatePinnedStatus(bot *tgbotapi.BotAPI, db *sql.DB, chatID, userID int64) {
	ctx := context.Background()
	text, err := formatStatusPanelText(ctx, db, userID)
	if err != nil {
		return
	}
	if bot == nil {
		return
	}
	var statusMsgID sql.NullInt64
	_ = db.QueryRowContext(ctx, `SELECT status_message_id FROM drivers WHERE user_id = ?1`, userID).Scan(&statusMsgID)
	if statusMsgID.Valid && statusMsgID.Int64 != 0 {
		edit := tgbotapi.NewEditMessageText(chatID, int(statusMsgID.Int64), text)
		if _, err := bot.Request(edit); err == nil {
			// Re-pin the status message so it always becomes the current pinned message.
			pin := tgbotapi.PinChatMessageConfig{ChatID: chatID, MessageID: int(statusMsgID.Int64)}
			if _, err := bot.Request(pin); err != nil {
				log.Printf("driver: re-pin status message: %v", err)
			}
			return
		} else {
			// Agar eski xabarni edit qilib bo'lmasa (o'chirilgan bo'lsa),
			// yangi status xabar yaratishga o'tamiz.
			log.Printf("driver: edit pinned status failed user_id=%d msg_id=%d: %v", userID, statusMsgID.Int64, err)
		}
	}
	// Create first status message and pin it once.
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = getDriverKeyboard(db, userID)
	sent, err := bot.Send(m)
	if err != nil {
		log.Printf("driver: send pinned status: %v", err)
		return
	}
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET status_message_id = ?1 WHERE user_id = ?2`, sent.MessageID, userID)
	pin := tgbotapi.PinChatMessageConfig{ChatID: chatID, MessageID: sent.MessageID}
	if _, err := bot.Request(pin); err != nil {
		log.Printf("driver: pin status message: %v", err)
	}
}

// sendWelcomeBonusMessageIfNeeded sends the welcome bonus explanation once after registration (if not already sent).
func sendWelcomeBonusMessageIfNeeded(bot *tgbotapi.BotAPI, db *sql.DB, chatID, userID int64) {
	ctx := context.Background()
	var sent int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(welcome_bonus_message_sent, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&sent); err != nil || sent != 0 {
		return
	}
	if _, err := bot.Send(tgbotapi.NewMessage(chatID, welcomeBonusMessage)); err != nil {
		log.Printf("driver: send welcome bonus message: %v", err)
		return
	}
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET welcome_bonus_message_sent = 1 WHERE user_id = ?1`, userID)
}

// UpdatePinnedStatusForChat updates the pinned driver status panel for the given chat (telegram user id). Called e.g. after trip finish from TripService.
func UpdatePinnedStatusForChat(bot *tgbotapi.BotAPI, db *sql.DB, chatID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, chatID).Scan(&userID); err != nil || userID == 0 {
		return
	}
	sendOrUpdatePinnedStatus(bot, db, chatID, userID)
}

func handleUpdate(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		handleCallback(bot, db, cfg, assignmentService, tripService, update.CallbackQuery)
		return
	}
	// Live location: Telegram sends repeated updates as edited_message with new coordinates.
	// Live end: edited_message with location.live_period == 0 or null.
	if update.EditedMessage != nil && update.EditedMessage.Location != nil {
		msg := update.EditedMessage
		if msg.From == nil {
			return
		}
		updateTime := time.Unix(int64(msg.EditDate), 0).UTC()
		if msg.EditDate == 0 {
			updateTime = time.Unix(int64(msg.Date), 0).UTC()
		}
		log.Printf("driver: live_location raw update chat_id=%d from_id=%d lat=%.6f lng=%.6f live_period=%d",
			msg.Chat.ID, msg.From.ID, msg.Location.Latitude, msg.Location.Longitude, msg.Location.LivePeriod)
		handleLiveLocationUpdate(bot, db, cfg, matchService, tripService, msg.Chat.ID, msg.From.ID, msg.Location, updateTime)
		return
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	chatID := msg.Chat.ID
	telegramID := msg.From.ID

	cmd := msg.Command()
	switch cmd {
	case "start":
		var referredBy *string
		if parts := strings.Fields(msg.Text); len(parts) > 1 && parts[1] != "" {
			if code := strings.TrimPrefix(parts[1], "ref_"); code != "" {
				referredBy = &code
			}
		}
		handleStart(bot, db, chatID, telegramID, referredBy)
		return
	case "status":
		handleStatus(bot, db, chatID, telegramID)
		return
	case "referral":
		handleReferral(bot, db, chatID, telegramID)
		return
	case "bonuslar":
		handleBonuslar(bot, db, chatID, telegramID)
		return
	case "leaderboard":
		handleLeaderboard(bot, db, chatID, telegramID)
		return
	case "online":
		handleOnline(bot, db, cfg, matchService, chatID, telegramID)
		return
	case "offline":
		handleOffline(bot, db, chatID, telegramID)
		return
	}

	// Application flow: phone -> first_name -> last_name -> car_type -> color -> plate -> license_photo -> vehicle_doc
	if msg.Contact != nil && handleApplicationText(bot, db, chatID, telegramID, msg.Contact.PhoneNumber) {
		return
	}
	if len(msg.Photo) > 0 {
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		if handleApplicationPhoto(bot, db, cfg, chatID, telegramID, fileID) {
			return
		}
	}

	// Handle keyboard button presses first so they always work (e.g. Live Location instruction even during registration).
	switch msg.Text {
	case btnOnline, btnStartWork:
		handleOnline(bot, db, cfg, matchService, chatID, telegramID)
		return
	case btnOffline, btnStopWork:
		handleOffline(bot, db, chatID, telegramID)
		return
	case btnLiveLocation:
		handleLiveLocationInstruction(bot, db, chatID, telegramID)
		return
	}

	if msg.Text != "" && handleApplicationText(bot, db, chatID, telegramID, msg.Text) {
		return
	}

	if msg.Location != nil {
		updateTime := time.Unix(int64(msg.Date), 0).UTC()
		handleLocation(bot, db, cfg, matchService, tripService, chatID, telegramID, msg.Location, false, updateTime)
		return
	}
}

func handleStart(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64, referredBy *string) {
	ctx := context.Background()
	code, err := utils.GenerateReferralCode(ctx, db)
	if err != nil {
		log.Printf("driver: generate referral code: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
		return
	}
	var refArg interface{}
	if referredBy != nil && *referredBy != "" {
		refArg = *referredBy
	}
	var userID int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO users (telegram_id, role, referral_code, referred_by) VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT (telegram_id) DO UPDATE SET
			role = excluded.role,
			referral_code = COALESCE(referral_code, excluded.referral_code),
			referred_by = COALESCE(referred_by, excluded.referred_by)
		RETURNING id`,
		telegramID, domain.RoleDriver, code, refArg).Scan(&userID)
	if err != nil {
		log.Printf("driver: upsert user: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
		return
	}
	// Driver referral: no reward on registration; full reward only after referred driver completes 5 trips and is active with live location (see trip_service FinishTrip).
	_, err = db.ExecContext(ctx, `
		INSERT INTO drivers (user_id, is_active) VALUES (?1, 0)
		ON CONFLICT (user_id) DO NOTHING`,
		userID)
	if err != nil {
		log.Printf("driver: ensure driver row: %v", err)
	}
	// Require driver application (phone, car type, color, plate) before location.
	// We infer the next step from missing fields so the bot still advances even if application_step isn't available.
	step, complete := inferApplicationStep(ctx, db, userID)
	if !complete {
		// Ask the current step's question only for "phone" (first step). For later steps, do not repeat the same question (e.g. "Mashina turi?") on every /start — wait for their answer (rule 6).
		if step == "phone" {
			sendApplicationPrompt(bot, db, chatID, userID, step)
		} else {
			send(bot, chatID, "Ilovani to'ldiring. Keyingi savolga javob yuboring.")
		}
		return
	}
	clearApplicationStep(ctx, db, userID)
	// Rewards and signup bonus are paid when docs are submitted (handleApplicationPhoto). Here we only show UI for returning drivers.
	// Application complete — show onboarding, welcome bonus message (once), and pinned status panel.
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, onboardingMessage)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send: %v", err)
	}
	sendWelcomeBonusMessageIfNeeded(bot, db, chatID, userID)
	sendOrUpdatePinnedStatus(bot, db, chatID, userID)
}

// inferApplicationStep returns the next application step and whether the application is complete.
// Registration includes: phone, first_name, last_name, car_type, color, plate, license_photo, vehicle_doc.
// Drivers with verification_status = 'approved' (e.g. existing before doc flow) are treated as complete without doc file_ids.
func inferApplicationStep(ctx context.Context, db *sql.DB, userID int64) (step string, complete bool) {
	var phone, firstName, lastName, carType, color, plate, licenseFileID, vehicleFileID, verificationStatus sql.NullString
	var appStep sql.NullString
	err := db.QueryRowContext(ctx, `SELECT phone, first_name, last_name, car_type, color, plate, license_photo_file_id, vehicle_doc_file_id, application_step, verification_status FROM drivers WHERE user_id = ?1`, userID).
		Scan(&phone, &firstName, &lastName, &carType, &color, &plate, &licenseFileID, &vehicleFileID, &appStep, &verificationStatus)
	if err != nil {
		_ = db.QueryRowContext(ctx, `SELECT phone, first_name, last_name, car_type, color, plate, application_step FROM drivers WHERE user_id = ?1`, userID).
			Scan(&phone, &firstName, &lastName, &carType, &color, &plate, &appStep)
		licenseFileID, vehicleFileID, verificationStatus = sql.NullString{}, sql.NullString{}, sql.NullString{}
	}

	missingPhone := !phone.Valid || strings.TrimSpace(phone.String) == ""
	missingFirstName := !firstName.Valid || strings.TrimSpace(firstName.String) == ""
	missingLastName := !lastName.Valid || strings.TrimSpace(lastName.String) == ""
	missingCarType := !carType.Valid || strings.TrimSpace(carType.String) == ""
	missingColor := !color.Valid || strings.TrimSpace(color.String) == ""
	missingPlate := !plate.Valid || strings.TrimSpace(plate.String) == ""
	missingLicense := !licenseFileID.Valid || strings.TrimSpace(licenseFileID.String) == ""
	missingVehicle := !vehicleFileID.Valid || strings.TrimSpace(vehicleFileID.String) == ""
	alreadyApproved := verificationStatus.Valid && strings.TrimSpace(verificationStatus.String) == "approved"

	if !missingPhone && !missingFirstName && !missingLastName && !missingCarType && !missingColor && !missingPlate && (!missingLicense && !missingVehicle || alreadyApproved) {
		return "", true
	}

	if appStep.Valid && strings.TrimSpace(appStep.String) != "" {
		s := strings.TrimSpace(appStep.String)
		switch s {
		case "phone":
			if missingPhone {
				return "phone", false
			}
		case "first_name":
			if missingFirstName {
				return "first_name", false
			}
		case "last_name":
			if missingLastName {
				return "last_name", false
			}
		case "car_type", "car_type_manual":
			if missingCarType {
				return s, false
			}
		case "color", "color_manual":
			if missingColor {
				return s, false
			}
		case "plate":
			if missingPlate {
				return "plate", false
			}
		case "license_photo":
			if missingLicense {
				return "license_photo", false
			}
		case "vehicle_doc":
			if missingVehicle {
				return "vehicle_doc", false
			}
		}
	}

	if missingPhone {
		setApplicationStep(ctx, db, userID, "phone")
		return "phone", false
	}
	if missingFirstName {
		setApplicationStep(ctx, db, userID, "first_name")
		return "first_name", false
	}
	if missingLastName {
		setApplicationStep(ctx, db, userID, "last_name")
		return "last_name", false
	}
	if missingCarType {
		setApplicationStep(ctx, db, userID, "car_type")
		return "car_type", false
	}
	if missingColor {
		setApplicationStep(ctx, db, userID, "color")
		return "color", false
	}
	if missingPlate {
		setApplicationStep(ctx, db, userID, "plate")
		return "plate", false
	}
	if missingLicense {
		setApplicationStep(ctx, db, userID, "license_photo")
		return "license_photo", false
	}
	setApplicationStep(ctx, db, userID, "vehicle_doc")
	return "vehicle_doc", false
}

func setApplicationStep(ctx context.Context, db *sql.DB, userID int64, step string) {
	// Best-effort: if column doesn't exist, ignore.
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, step, userID)
}

func clearApplicationStep(ctx context.Context, db *sql.DB, userID int64) {
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET application_step = NULL WHERE user_id = ?1`, userID)
}

// sendApplicationPrompt sends the next question for the driver application (phone -> first_name -> last_name -> car_type -> color -> plate).
// For the phone step, shows a "Share number" button; for car_type/color, shows button keyboards.
func sendApplicationPrompt(bot *tgbotapi.BotAPI, db *sql.DB, chatID int64, driverUserID int64, step string) {
	switch step {
	case "phone":
		text := "Ilovani to'ldiring. Telefon raqamingizni yuboring (tugmani bosing yoki yozing)."
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButtonContact("📞 Telefon raqamini yuborish"),
			),
		)
		kb.ResizeKeyboard = true
		kb.OneTimeKeyboard = true
		m := tgbotapi.NewMessage(chatID, text)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send application prompt: %v", err)
		}
		return
	case "first_name":
		send(bot, chatID, "Ismingizni kiriting")
		return
	case "last_name":
		send(bot, chatID, "Familyangizni kiriting")
		return
	case "car_type":
		text := "Mashina turini tanlang yoki «Boshqa» bosing va yozing."
		kb := carTypeKeyboard()
		m := tgbotapi.NewMessage(chatID, text)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send application prompt: %v", err)
		}
		return
	case "car_type_manual":
		send(bot, chatID, "Mashina modelini yozing.")
		return
	case "color":
		text := "Mashina rangini tanlang yoki «Boshqa» bosing va yozing."
		kb := colorKeyboard()
		m := tgbotapi.NewMessage(chatID, text)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send application prompt: %v", err)
		}
		return
	case "color_manual":
		send(bot, chatID, "Mashina rangini yozing.")
		return
	case "plate":
		text := "🚘 Davlat raqamingizni to‘liq kiriting\n\nMasalan: 20N306ZB"
		send(bot, chatID, text)
		return
	case "license_photo":
		text := "📄 Haydovchilik guvohnomasi\n\nIltimos, haydovchilik guvohnomangiz rasmini yuboring.\n\nTalablar:\n• rasm aniq bo'lsin\n• barcha ma'lumotlar ko'rinsin"
		send(bot, chatID, text)
		return
	case "vehicle_doc":
		text := "🚗 Tex pasport\n\nMashinaning tex pasporti rasmini yuboring.\n\nTalablar:\n• rasm aniq bo'lsin\n• davlat raqami ko'rinsin"
		send(bot, chatID, text)
		return
	default:
		_, _ = db.ExecContext(context.Background(), `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, step, driverUserID)
		send(bot, chatID, "Telefon raqamingizni yuboring.")
	}
}

// handleApplicationPhoto handles photo uploads for license_photo and vehicle_doc steps. Returns true if the message was consumed.
func handleApplicationPhoto(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, telegramID int64, fileID string) bool {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
		return false
	}
	step, complete := inferApplicationStep(ctx, db, userID)
	if complete || (step != "license_photo" && step != "vehicle_doc") {
		return false
	}
	if step == "license_photo" {
		_, err := db.ExecContext(ctx, `UPDATE drivers SET license_photo_file_id = ?1, application_step = 'vehicle_doc' WHERE user_id = ?2`, fileID, userID)
		if err != nil {
			log.Printf("driver: save license photo: %v", err)
			return true
		}
		sendApplicationPrompt(bot, db, chatID, userID, "vehicle_doc")
		return true
	}
	// vehicle_doc
	_, err := db.ExecContext(ctx, `UPDATE drivers SET vehicle_doc_file_id = ?1, verification_status = 'pending_approval', application_step = NULL WHERE user_id = ?2`, fileID, userID)
	if err != nil {
		log.Printf("driver: save vehicle doc: %v", err)
		return true
	}
	log.Printf("driver: registration docs saved user_id=%d", userID)
	log.Printf("driver: status changed to pending_approval user_id=%d", userID)

	// Load driver info for admin approval request.
	var firstName, lastName, phone, carModel, color, plateNumber sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(first_name, ''), COALESCE(last_name, ''), COALESCE(phone, ''), COALESCE(car_type, ''), COALESCE(color, ''), COALESCE(plate_number, '')
		FROM drivers WHERE user_id = ?1`, userID).Scan(&firstName, &lastName, &phone, &carModel, &color, &plateNumber); err != nil {
		log.Printf("driver: load driver info for admin approval user_id=%d: %v", userID, err)
	} else if cfg != nil && cfg.AdminID != 0 && cfg.AdminBotToken != "" {
		fullName := strings.TrimSpace(strings.TrimSpace(firstName.String) + " " + strings.TrimSpace(lastName.String))
		adminText := fmt.Sprintf(
			"🚕 Yangi haydovchi tasdiqlash uchun\n\n👤 Ism familiya: %s\n📞 Telefon: %s\n🚗 Mashina: %s\n🎨 Rang: %s\n🔢 Raqam: %s\n👤 Telegram ID: %d\n\n📄 Hujjatlar quyida",
			fullName, phone.String, carModel.String, color.String, plateNumber.String, telegramID,
		)
		adminChatID := cfg.AdminID
		adminBot, err := tgbotapi.NewBotAPI(cfg.AdminBotToken)
		if err != nil {
			log.Printf("driver: create admin bot for approval user_id=%d: %v", userID, err)
		} else {
			// Header text via admin bot
			if _, err := adminBot.Send(tgbotapi.NewMessage(adminChatID, adminText)); err != nil {
				log.Printf("driver: admin approval header send error user_id=%d: %v", userID, err)
			} else {
				log.Printf("driver: admin approval header sent user_id=%d", userID)
			}

			// Photos via admin bot: download from driver bot and re-upload as bytes.
			if bot != nil {
				// License photo
				var licenseID sql.NullString
				_ = db.QueryRowContext(ctx, `SELECT license_photo_file_id FROM drivers WHERE user_id = ?1`, userID).Scan(&licenseID)
				if licenseID.Valid && licenseID.String != "" {
					if f, err := bot.GetFile(tgbotapi.FileConfig{FileID: licenseID.String}); err != nil {
						log.Printf("driver: getFile license error user_id=%d: %v", userID, err)
					} else if f.FilePath != "" {
						url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", cfg.DriverBotToken, f.FilePath)
						if resp, err := http.Get(url); err != nil {
							log.Printf("driver: download license photo error user_id=%d: %v", userID, err)
						} else {
							defer resp.Body.Close()
							data, err := io.ReadAll(resp.Body)
							if err != nil {
								log.Printf("driver: read license photo error user_id=%d: %v", userID, err)
							} else {
								photo := tgbotapi.NewPhoto(adminChatID, tgbotapi.FileBytes{
									Name:  "license.jpg",
									Bytes: data,
								})
								if _, err := adminBot.Send(photo); err != nil {
									log.Printf("driver: admin license photo send error user_id=%d: %v", userID, err)
								} else {
									log.Printf("driver: admin license photo sent user_id=%d", userID)
								}
							}
						}
					}
				}
				// Vehicle document (current fileID)
				if f, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID}); err != nil {
					log.Printf("driver: getFile vehicle doc error user_id=%d: %v", userID, err)
				} else if f.FilePath != "" {
					url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", cfg.DriverBotToken, f.FilePath)
					if resp, err := http.Get(url); err != nil {
						log.Printf("driver: download vehicle doc photo error user_id=%d: %v", userID, err)
					} else {
						defer resp.Body.Close()
						data, err := io.ReadAll(resp.Body)
						if err != nil {
							log.Printf("driver: read vehicle doc photo error user_id=%d: %v", userID, err)
						} else {
							photo := tgbotapi.NewPhoto(adminChatID, tgbotapi.FileBytes{
								Name:  "vehicle_doc.jpg",
								Bytes: data,
							})
							if _, err := adminBot.Send(photo); err != nil {
								log.Printf("driver: admin vehicle doc photo send error user_id=%d: %v", userID, err)
							} else {
								log.Printf("driver: admin vehicle doc photo sent user_id=%d", userID)
							}
						}
					}
				}
			}

			// Instruction text + inline buttons via admin bot (callbacks handled by admin bot).
			kb := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("✅ Approve", fmt.Sprintf("approve_driver_%d", userID)),
					tgbotapi.NewInlineKeyboardButtonData("❌ Reject", fmt.Sprintf("reject_driver_%d", userID)),
				),
			)
			inlineMsg := tgbotapi.NewMessage(adminChatID, "Haydovchini tasdiqlang yoki rad eting.")
			inlineMsg.ReplyMarkup = kb
			if _, err := adminBot.Send(inlineMsg); err != nil {
				log.Printf("driver: admin approval inline buttons send error user_id=%d: %v", userID, err)
			} else {
				log.Printf("driver: admin approval buttons sent via admin bot user_id=%d", userID)
			}
		}
	}

	// Notify driver.
		send(bot, chatID, "✅ Ma’lumotlaringiz qabul qilindi.\nAdmin tasdiqlashidan so‘ng sizga xabar beriladi.")
	rewardReferrerOnApplicationComplete(bot, db, userID)
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET balance = balance + 100000, signup_bonus_paid = 1 WHERE user_id = ?1 AND COALESCE(signup_bonus_paid, 0) = 0`, userID)
	sendWelcomeBonusMessageIfNeeded(bot, db, chatID, userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "Tasdiqlash kutilmoqda. Holatni /status buyrug'i orqali tekshiring.")
	m.ReplyMarkup = kb
	_, _ = bot.Send(m)
	return true
}

func handleApplicationText(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64, text string) bool {
	ctx := context.Background()
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil || userID == 0 {
		return false
	}

	// Determine step from application_step if present, otherwise infer from missing fields.
	step, complete := inferApplicationStep(ctx, db, userID)
	if complete || step == "" {
		return false
	}

	switch step {
	case "phone":
		// One driver per phone number (prevent multiple accounts for fake referrals).
		var otherUserID int64
		err := db.QueryRowContext(ctx, `SELECT user_id FROM drivers WHERE phone = ?1 AND user_id != ?2 LIMIT 1`, text, userID).Scan(&otherUserID)
		if err == nil {
			send(bot, chatID, "Bu telefon raqami allaqachon ro'yxatdan o'tgan. Boshqa raqamdan foydalaning.")
			return true
		}
		_, err = db.ExecContext(ctx, `UPDATE drivers SET phone = ?1, application_step = 'first_name' WHERE user_id = ?2`, text, userID)
		if err != nil {
			log.Printf("driver: save phone: %v", err)
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET phone = ?1 WHERE user_id = ?2`, text, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "first_name")
	case "first_name":
		_, err = db.ExecContext(ctx, `UPDATE drivers SET first_name = ?1, application_step = 'last_name' WHERE user_id = ?2`, text, userID)
		if err != nil {
			log.Printf("driver: save first_name: %v", err)
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET first_name = ?1 WHERE user_id = ?2`, text, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "last_name")
	case "last_name":
		_, err = db.ExecContext(ctx, `UPDATE drivers SET last_name = ?1, application_step = 'car_type' WHERE user_id = ?2`, text, userID)
		if err != nil {
			log.Printf("driver: save last_name: %v", err)
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET last_name = ?1 WHERE user_id = ?2`, text, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "car_type")
	case "car_type":
		if text == carTypeBoshqa {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, "car_type_manual", userID)
			send(bot, chatID, "Mashina modelini yozing.")
		} else {
			_, err = db.ExecContext(ctx, `UPDATE drivers SET car_type = ?1, application_step = 'color' WHERE user_id = ?2`, text, userID)
			if err != nil {
				_, _ = db.ExecContext(ctx, `UPDATE drivers SET car_type = ?1 WHERE user_id = ?2`, text, userID)
			}
			sendApplicationPrompt(bot, db, chatID, userID, "color")
		}
	case "car_type_manual":
		_, err = db.ExecContext(ctx, `UPDATE drivers SET car_type = ?1, application_step = 'color' WHERE user_id = ?2`, text, userID)
		if err != nil {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET car_type = ?1 WHERE user_id = ?2`, text, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "color")
	case "color":
		if text == "Boshqa" {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, "color_manual", userID)
			send(bot, chatID, "Mashina rangini yozing.")
		} else {
			_, err = db.ExecContext(ctx, `UPDATE drivers SET color = ?1, application_step = 'plate' WHERE user_id = ?2`, text, userID)
			if err != nil {
				_, _ = db.ExecContext(ctx, `UPDATE drivers SET color = ?1 WHERE user_id = ?2`, text, userID)
			}
			sendApplicationPrompt(bot, db, chatID, userID, "plate")
		}
	case "color_manual":
		_, err = db.ExecContext(ctx, `UPDATE drivers SET color = ?1, application_step = 'plate' WHERE user_id = ?2`, text, userID)
		if err != nil {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET color = ?1 WHERE user_id = ?2`, text, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "plate")
	case "plate":
		plate := strings.ToUpper(strings.TrimSpace(text))
		matched, _ := regexp.MatchString(`^[0-9]{2}[A-Z]{1}[0-9]{3}[A-Z]{2}$`, plate)
		if !matched {
			send(bot, chatID, "❌ Noto‘g‘ri raqam formati.\n\nTo‘g‘ri format: 20N306ZB\nIltimos, davlat raqamini to‘liq kiriting.")
			return true
		}
		log.Printf("driver: plate validated user_id=%d plate=%s", userID, plate)
		_, err = db.ExecContext(ctx, `UPDATE drivers SET plate = ?1, plate_number = ?1, application_step = 'license_photo' WHERE user_id = ?2`, plate, userID)
		if err != nil {
			log.Printf("driver: save plate: %v", err)
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET plate = ?1, plate_number = ?1, application_step = 'license_photo' WHERE user_id = ?2`, plate, userID)
		}
		sendApplicationPrompt(bot, db, chatID, userID, "license_photo")
	default:
		return false
	}
	return true
}

func handleOnline(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, chatID, telegramID int64) {
	ctx := context.Background()
	// Require driver application (phone, car type, color, plate) and location
	var phone, carType, color, plate sql.NullString
	var lastLat, lastLng sql.NullFloat64
	_ = db.QueryRowContext(ctx, `
		SELECT d.phone, d.car_type, d.color, d.plate, d.last_lat, d.last_lng
		FROM drivers d JOIN users u ON u.id = d.user_id WHERE u.telegram_id = ?1`,
		telegramID).Scan(&phone, &carType, &color, &plate, &lastLat, &lastLng)
	if !phone.Valid || phone.String == "" || !carType.Valid || carType.String == "" || !color.Valid || color.String == "" || !plate.Valid || plate.String == "" {
		send(bot, chatID, "Avval ilovani to'ldiring: /start bosing va telefon, mashina turi, rangi, davlat raqamini yuboring.")
		return
	}
	var userID int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if userID != 0 {
		var verificationStatus sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, userID).Scan(&verificationStatus)
		if !verificationStatus.Valid || strings.TrimSpace(verificationStatus.String) != "approved" {
			kb := driverKeyboardForVerificationPending()
			m := tgbotapi.NewMessage(chatID, "Tasdiqlash kutilmoqda. Admin profilingizni tekshirmoqda.")
			m.ReplyMarkup = kb
			if _, err := bot.Send(m); err != nil {
				log.Printf("driver: send pending verification online message: %v", err)
			}
			return
		}
	}
	if userID != 0 && !isDriverBalanceSufficient(ctx, db, userID, cfg) {
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, insufficientBalanceMessage)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send: %v", err)
		}
		return
	}
	// Require active live location before going online and starting bonus.
	var liveActive int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(live_location_active, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&liveActive)
	if liveActive == 0 {
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, "Online bo'lish uchun avval jonli lokatsiyani yoqing.\n\n" + liveLocationBilingualInstruction)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send require live location: %v", err)
		}
		return
	}

	_, err := db.ExecContext(ctx, `
		UPDATE drivers SET is_active = 1, manual_offline = 0, last_seen_at = ?1
		WHERE user_id = (SELECT id FROM users WHERE telegram_id = ?2)`,
		time.Now().UTC().Format("2006-01-02 15:04:05"), telegramID)
	if err != nil {
		log.Printf("driver: online: %v", err)
		send(bot, chatID, "Xatolik.")
		return
	}
	sendOrUpdatePinnedStatus(bot, db, chatID, userID)
	// Message with keyboard and online bonus explanation.
	kb := driverKeyboardForStatus(true)
	m := tgbotapi.NewMessage(chatID, "🟢 Siz onlinesiz.\n\n💰 Online bonus ishlayapti.\n1 soat → +2 000 so'm\n\nBugungi limit:\n20 000 so'm")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send: %v", err)
	}
	if shouldShowOnlineNoLiveReminder(ctx, db, userID) {
		nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, userID)
	}
	if userID != 0 && matchService != nil {
		go matchService.NotifyDriverOfPendingRequests(context.Background(), userID)
	}
}

func handleOffline(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		UPDATE drivers SET is_active = 0, manual_offline = 1
		WHERE user_id = (SELECT id FROM users WHERE telegram_id = ?1)`,
		telegramID)
	if err != nil {
		log.Printf("driver: offline: %v", err)
		send(bot, chatID, "Xatolik.")
		return
	}
	var userID int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	sendOrUpdatePinnedStatus(bot, db, chatID, userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "🔴 Siz oflaynsiz.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send: %v", err)
	}
}

// rewardReferrerOnApplicationComplete notifies the referrer when a referred driver completes registration (docs submitted).
// It does NOT add any balance; actual referral reward is paid in TripService.FinishTrip after 5 successful trips with live location.
func rewardReferrerOnApplicationComplete(bot *tgbotapi.BotAPI, db *sql.DB, newDriverUserID int64) {
	ctx := context.Background()
	var referredBy sql.NullString
	var stage1Paid int
	if err := db.QueryRowContext(ctx, `SELECT referred_by, COALESCE(referral_stage1_reward_paid, 0) FROM users WHERE id = ?1`, newDriverUserID).Scan(&referredBy, &stage1Paid); err != nil || !referredBy.Valid || referredBy.String == "" || stage1Paid != 0 {
		return
	}
	var referrerUserID, referrerTelegramID int64
	if err := db.QueryRowContext(ctx, `SELECT id, telegram_id FROM users WHERE referral_code = ?1`, referredBy.String).Scan(&referrerUserID, &referrerTelegramID); err != nil || referrerUserID == 0 {
		return
	}
	// Mark this referred user as stage1 processed (notification sent).
	_, _ = db.ExecContext(ctx, `UPDATE users SET referral_stage1_reward_paid = 1 WHERE id = ?1`, newDriverUserID)
	var newDriverName string
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(TRIM(name), ''), 'Yangi haydovchi') FROM users WHERE id = ?1`, newDriverUserID).Scan(&newDriverName)
	if referrerTelegramID != 0 && bot != nil {
		msg := fmt.Sprintf("🎉 Do'stingiz %s taklif havolangiz orqali haydovchi bo'lib ro'yxatdan o'tdi.\n\nU 5 ta safar bajargandan keyin siz\n100 000 so'm referral bonus olasiz.", newDriverName)
		if _, err := bot.Send(tgbotapi.NewMessage(referrerTelegramID, msg)); err != nil {
			log.Printf("driver: notify referrer: %v", err)
		}
	}
}

// handleReferral sends an invitation-style message with the shareable link only (for forwarding to others).
// If the user has no referral_code, one is generated and saved (backfill).
func handleReferral(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	var referralCode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT u.id, u.referral_code FROM users u WHERE u.telegram_id = ?1`, telegramID).Scan(&userID, &referralCode); err != nil || userID == 0 {
		send(bot, chatID, "Avval /start bosing.")
		return
	}
	code := referralCode.String
	if !referralCode.Valid || code == "" {
		var err error
		code, err = utils.GenerateReferralCode(ctx, db)
		if err != nil {
			log.Printf("driver: generate referral code for /referral: %v", err)
			send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
			return
		}
		if _, err := db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, userID); err != nil {
			log.Printf("driver: save referral code: %v", err)
			send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
			return
		}
	}
	botUsername := ""
	if bot != nil {
		botUsername = bot.Self.UserName
	}
	shareLink := utils.ReferralLink(botUsername, code)
	text := "🎁 Haydovchi taklif qiling\n\nTaklif qilgan har bir haydovchi uchun\n100 000 so'm bonus olasiz\n(5 ta safar bajargandan keyin)\n\nSizning referral havolangiz:\n" + shareLink
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send referral: %v", err)
	}
}

// handleBonuslar shows referral stats: referred count and bonus (for /bonuslar command).
func handleBonuslar(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	var referralCode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT u.id, u.referral_code FROM users u WHERE u.telegram_id = ?1`, telegramID).Scan(&userID, &referralCode); err != nil || userID == 0 {
		send(bot, chatID, "Avval /start bosing.")
		return
	}
	code := referralCode.String
	if !referralCode.Valid || code == "" {
		var err error
		code, err = utils.GenerateReferralCode(ctx, db)
		if err != nil {
			log.Printf("driver: generate referral code for /bonuslar: %v", err)
			send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
			return
		}
		if _, err := db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, userID); err != nil {
			log.Printf("driver: save referral code: %v", err)
			send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
			return
		}
	}
	var referredCount int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u INNER JOIN drivers d ON d.user_id = u.id WHERE u.referred_by = ?1`, code).Scan(&referredCount)
	var stage1Count int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referred_by = ?1 AND COALESCE(referral_stage1_reward_paid, 0) = 1`, code).Scan(&stage1Count)
	var stage2Count int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referred_by = ?1 AND COALESCE(referral_stage2_reward_paid, 0) = 1`, code).Scan(&stage2Count)
	// Stage1 no longer pays money; only stage2 (100k after 5 trips) contributes to earnings.
	totalEarnings := stage2Count * 100000
	text := fmt.Sprintf("📊 Referral statistikasi\n\nTaklif qilgan haydovchilar: %d\nReferral bonus: %d so'm", referredCount, totalEarnings)
	text += "\n\n🔗 Taklif havolasi: /referral"
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send bonuslar: %v", err)
	}
}

// handleLeaderboard replies to /leaderboard with top drivers by referred driver count.
func handleLeaderboard(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(TRIM(u.name), ''), 'Haydovchi') AS name,
		       (SELECT COUNT(*) FROM users u2 INNER JOIN drivers d2 ON d2.user_id = u2.id WHERE u2.referred_by = u.referral_code) AS cnt
		FROM users u
		INNER JOIN drivers d ON d.user_id = u.id
		WHERE u.referral_code IS NOT NULL AND u.referral_code != ''
		ORDER BY cnt DESC
		LIMIT 10`)
	if err != nil {
		log.Printf("driver: leaderboard query: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
		return
	}
	defer rows.Close()
	var lines []string
	rank := 1
	for rows.Next() {
		var name string
		var cnt int64
		if err := rows.Scan(&name, &cnt); err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s — %d ta driver", rank, name, cnt))
		rank++
	}
	if err := rows.Err(); err != nil {
		log.Printf("driver: leaderboard rows: %v", err)
	}
	text := "🏆 Eng faol haydovchilar\n\n"
	if len(lines) == 0 {
		text += "Hali ma'lumot yo'q."
	} else {
		text += strings.Join(lines, "\n")
	}
	var userID int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send leaderboard: %v", err)
	}
}

// handleStatus replies with the same layout as the pinned status panel.
func handleStatus(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
		send(bot, chatID, "Avval /start bosing.")
		return
	}
	// Render the current status text and send it as a message,
	// and also update the pinned status panel.
	text, err := formatStatusPanelText(ctx, db, userID)
	if err != nil {
		send(bot, chatID, "Xatolik.")
		return
	}
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send status: %v", err)
	}
	sendOrUpdatePinnedStatus(bot, db, chatID, userID)
}

func handleRequestLocation(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "Lokatsiyani Telegramda 📎 → Location orqali yuboring.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send: %v", err)
	}
}

// handleLiveLocationUpdate processes edited_message.location (live update or live end) or message.location with live_period (live start).
// If loc.LivePeriod <= 0 when from edited_message, treats as live end: sets live_location_active = 0 and returns.
func handleLiveLocationUpdate(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, tripService *services.TripService, chatID, telegramID int64, loc *tgbotapi.Location, updateTime time.Time) {
	ctx := context.Background()
	// Live end: edited_message with location.live_period null/0 — stop accepting updates and clear live state; send one-time warning.
	if loc != nil && loc.LivePeriod <= 0 {
		var userID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil {
			return
		}
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_active = 0, last_live_location_at = NULL WHERE user_id = ?1`, userID)
		log.Printf("driver: live_location end user_id=%d", userID)
		sendOrUpdatePinnedStatus(bot, db, chatID, userID)
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, "📍 Jonli lokatsiya o'chdi.\n\n⚠️ Buyurtmalar kelmaydi.\n\nQayta jonli lokatsiya ulash uchun:\n📎 → Location → Share Live Location")
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send: %v", err)
		}
		return
	}
	lat, lng := loc.Latitude, loc.Longitude
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("driver: get user (live): %v", err)
		}
		return
	}
	var verificationStatus sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, userID).Scan(&verificationStatus)
	if !verificationStatus.Valid || strings.TrimSpace(verificationStatus.String) != "approved" {
		// Ignore live updates for unapproved drivers (no auto-online, no dispatch).
		return
	}

	// During registration: only update position/live state; do not send confirmation, set online, or dispatch (rules 1–3).
	if !isDriverFullyRegistered(ctx, db, userID) {
		gridID := utils.GridID(lat, lng)
		nowStr := updateTime.UTC().Format("2006-01-02 15:04:05")
		_, _ = db.ExecContext(ctx, `
			UPDATE drivers SET last_lat = ?1, last_lng = ?2, last_seen_at = ?3, grid_id = ?4, last_live_location_at = ?5, live_location_active = 1 WHERE user_id = ?6`,
			lat, lng, nowStr, gridID, nowStr, userID)
		return
	}

	var prevLat, prevLng sql.NullFloat64
	var lastSeenAt, lastLiveAt sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT last_lat, last_lng, last_seen_at, last_live_location_at FROM drivers WHERE user_id = ?1`, userID).Scan(&prevLat, &prevLng, &lastSeenAt, &lastLiveAt)
	stale := false
	if lastSeenAt.Valid && lastSeenAt.String != "" {
		if parsed, err := parseUTC(lastSeenAt.String); err == nil && !updateTime.After(parsed) {
			log.Printf("driver: live_location ignored stale user_id=%d", userID)
			stale = true
		}
	}
	// Live was inactive if last_live_location_at is null or older than 90s; then this update "activates" live — send confirmation once (only when fully registered).
	wasLiveActive := false
	if lastLiveAt.Valid && lastLiveAt.String != "" {
		if t, err := parseUTC(lastLiveAt.String); err == nil && time.Since(t) <= time.Duration(liveLocationActiveSeconds)*time.Second {
			wasLiveActive = true
		}
	}

	gridID := utils.GridID(lat, lng)
	nowStr := updateTime.UTC().Format("2006-01-02 15:04:05")
	if !stale {
		_, _ = db.ExecContext(ctx, `
			UPDATE drivers SET last_lat = ?1, last_lng = ?2, last_seen_at = ?3, grid_id = ?4, last_live_location_at = ?5, live_location_active = 1 WHERE user_id = ?6`,
			lat, lng, nowStr, gridID, nowStr, userID)
	} else {
		// Still extend live window so 90s eligibility is maintained
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_active = 1, last_live_location_at = ?1 WHERE user_id = ?2`, nowStr, userID)
	}
	// Update pinned panel when live becomes active and also show the current
	// status card once so the driver clearly sees the new state.
	if !wasLiveActive {
		sendOrUpdatePinnedStatus(bot, db, chatID, userID)
		text, err := formatStatusPanelText(ctx, db, userID)
		if err == nil {
			kb := getDriverKeyboard(db, userID)
			m := tgbotapi.NewMessage(chatID, text)
			m.ReplyMarkup = kb
			if _, err := bot.Send(m); err != nil {
				log.Printf("driver: send live status card: %v", err)
			}
		}
	}

	// If driver has STARTED trip, add point (no chat message)
	var startedTripID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM trips WHERE driver_user_id = ?1 AND status = ?2 LIMIT 1`, userID, domain.TripStatusStarted).Scan(&startedTripID); err == nil && startedTripID != "" && tripService != nil {
		_, _, _ = tripService.AddPoint(ctx, startedTripID, userID, lat, lng, time.Now())
		log.Printf("driver: live_location trip_point user_id=%d trip_id=%s lat=%.6f lng=%.6f", userID, startedTripID, lat, lng)
		return
	}
	// If WAITING trip, do nothing (no spam)
	var waitingTripID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM trips WHERE driver_user_id = ?1 AND status = ?2 LIMIT 1`, userID, domain.TripStatusWaiting).Scan(&waitingTripID); err == nil && waitingTripID != "" {
		log.Printf("driver: live_location skip_dispatch user_id=%d trip_status=WAITING trip_id=%s", userID, waitingTripID)
		return
	}

	// No active trip: only set online and push pending requests if driver is not manually offline (rule 5: while offline, live updates update position but do not send ride requests).
	var manualOffline int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(manual_offline, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&manualOffline)
	if manualOffline == 0 && isDriverBalanceSufficient(ctx, db, userID, cfg) {
		onlineNowStr := updateTime.UTC().Format("2006-01-02 15:04:05")
		if stale {
			onlineNowStr = time.Now().UTC().Format("2006-01-02 15:04:05")
		}
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET is_active = 1, manual_offline = 0, last_seen_at = ?1 WHERE user_id = ?2`,
			onlineNowStr, userID)
		if matchService != nil {
			log.Printf("driver: live_location auto_online user_id=%d lat=%.6f lng=%.6f grid=%s", userID, lat, lng, gridID)
			go matchService.NotifyDriverOfPendingRequests(context.Background(), userID)
		}
		// Movement-based dispatch when driver moved ~300m (only if we set them online).
		const minMovementM = 300.0
		if matchService != nil && prevLat.Valid && prevLng.Valid {
			distM := utils.HaversineMeters(prevLat.Float64, prevLng.Float64, lat, lng)
			if distM >= minMovementM {
				go func(driverID int64) {
					matchService.NotifyDriverOfPendingRequests(context.Background(), driverID)
				}(userID)
			}
		}
	} else {
		// Driver is offline but sharing Live Location: remind to go online (once per cooldown).
		sendOfflineButLiveReminderIfNeeded(bot, db, chatID, userID)
	}
}

// handleLocation processes message.location. Only Telegram Live Location is accepted; static location is rejected.
func handleLocation(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, tripService *services.TripService, chatID, telegramID int64, loc *tgbotapi.Location, silent bool, updateTime time.Time) {
	if loc == nil {
		return
	}
	// Static location: live_period is null/0 — do not update coordinates or last_seen_at; send rejection once per cooldown to avoid spam.
	if loc.LivePeriod <= 0 {
		if !silent {
			ctx := context.Background()
			var userID int64
			if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
				return
			}
			var lastRej sql.NullString
			_ = db.QueryRowContext(ctx, `SELECT static_location_rejection_last_sent_at FROM drivers WHERE user_id = ?1`, userID).Scan(&lastRej)
			if lastRej.Valid && lastRej.String != "" {
				if t, err := parseUTC(lastRej.String); err == nil && time.Since(t) < 2*time.Minute {
					return
				}
			}
			kb := getDriverKeyboard(db, userID)
			m := tgbotapi.NewMessage(chatID, staticLocationRejectionMessage)
			m.ReplyMarkup = kb
			if _, err := bot.Send(m); err != nil {
				return
			}
			nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET static_location_rejection_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, userID)
		}
		return
	}
	// Live location start: live_period set — same handling as live update.
	handleLiveLocationUpdate(bot, db, cfg, matchService, tripService, chatID, telegramID, loc, updateTime)
}

func handleCallback(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, assignmentService *services.AssignmentService, tripService *services.TripService, q *tgbotapi.CallbackQuery) {
	chatID := q.Message.Chat.ID
	telegramID := q.From.ID
	data := q.Data

	if strings.HasPrefix(data, cbAccept) {
		requestID := strings.TrimPrefix(data, cbAccept)
		handleAccept(bot, db, cfg, assignmentService, tripService, chatID, telegramID, requestID, q)
	} else if strings.HasPrefix(data, "approve_driver_") || strings.HasPrefix(data, "reject_driver_") {
		if cfg == nil || telegramID != cfg.AdminID {
			if q.ID != "" {
				_, _ = bot.Request(tgbotapi.NewCallback(q.ID, "Ruxsat yo'q"))
			}
			return
		}
		parts := strings.Split(data, "_")
		if len(parts) < 3 {
			return
		}
		driverIDStr := parts[len(parts)-1]
		driverUserID, err := strconv.ParseInt(driverIDStr, 10, 64)
		if err != nil || driverUserID <= 0 {
			return
		}
		ctx := context.Background()
		switch {
		case strings.HasPrefix(data, "approve_driver_"):
			var status string
			var notified int
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(verification_status, ''), COALESCE(approval_notified, 0) FROM drivers WHERE user_id = ?1`, driverUserID).Scan(&status, &notified); err != nil {
				log.Printf("driver: load driver status for approve user_id=%d: %v", driverUserID, err)
				return
			}
			if status == "approved" {
				// Already approved; do not send duplicate notification.
				return
			}
			if _, err := db.ExecContext(ctx, `UPDATE drivers SET verification_status = 'approved' WHERE user_id = ?1`, driverUserID); err != nil {
				log.Printf("driver: approve driver update error user_id=%d: %v", driverUserID, err)
				return
			}
			log.Printf("driver: driver approved by admin user_id=%d", driverUserID)
			if notified != 0 {
				// Approval already notified via some other path.
				return
			}
			var driverTgID int64
			if err := db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&driverTgID); err != nil || driverTgID == 0 {
				return
			}
			msg := tgbotapi.NewMessage(driverTgID, "🎉 Profilingiz tasdiqlandi!\n\nEndi siz buyurtmalar qabul qilishingiz mumkin.\n\n🟢 Ishni boshlash\n📡 Jonli lokatsiyani yoqing")
			if _, err := bot.Send(msg); err != nil {
				log.Printf("driver: notify approved driver send error user_id=%d: %v", driverUserID, err)
				return
			}
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET approval_notified = 1 WHERE user_id = ?1`, driverUserID)
		case strings.HasPrefix(data, "reject_driver_"):
			if _, err := db.ExecContext(ctx, `UPDATE drivers SET verification_status = 'rejected', license_photo_file_id = NULL, vehicle_doc_file_id = NULL, application_step = 'license_photo' WHERE user_id = ?1 AND verification_status != 'approved'`, driverUserID); err != nil {
				log.Printf("driver: reject driver update error user_id=%d: %v", driverUserID, err)
			} else {
				log.Printf("driver: driver rejected by admin user_id=%d", driverUserID)
				var driverTgID int64
				if err := db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&driverTgID); err == nil && driverTgID != 0 {
					msg := tgbotapi.NewMessage(driverTgID, "❌ Hujjatlaringiz tasdiqlanmadi.\nIltimos, aniqroq rasm yuboring.")
					if _, err := bot.Send(msg); err != nil {
						log.Printf("driver: notify rejected driver send error user_id=%d: %v", driverUserID, err)
					}
				}
			}
		}
	}
	if q.ID != "" {
		bot.Request(tgbotapi.NewCallback(q.ID, ""))
	}
}

func handleAccept(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, assignmentService *services.AssignmentService, tripService *services.TripService, chatID, telegramID int64, requestID string, q *tgbotapi.CallbackQuery) {
	ctx := context.Background()
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil || userID == 0 {
		send(bot, chatID, "Xatolik.")
		return
	}
	if assignmentService == nil {
		send(bot, chatID, "Xatolik.")
		return
	}
	assigned, tripID, err := assignmentService.TryAssign(ctx, requestID, userID)
	if err != nil {
		log.Printf("driver: TryAssign: %v", err)
		send(bot, chatID, "Xatolik.")
		return
	}
	if !assigned {
		send(bot, chatID, "So'rov allaqachon boshqaga berilgan yoki bekor qilingan.")
		return
	}
	if tripService != nil {
		tripService.ScheduleStartReminder(ctx, tripID, userID)
	}
	// Send "Open Trip Map" Web App button so driver can open Mini App
	sendWithOpenTripMapButton(bot, chatID, cfg, tripID, userID)
}

// webAppKeyboard is used to send an inline button that opens the Telegram Mini App (web_app).
// The standard library InlineKeyboardButton only has URL, not web_app, so we use a custom type.
type webAppKeyboard struct {
	InlineKeyboard [][]webAppButton `json:"inline_keyboard"`
}
type webAppButton struct {
	Text   string     `json:"text"`
	WebApp *webAppInfo `json:"web_app,omitempty"`
}
type webAppInfo struct {
	URL string `json:"url"`
}

func sendWithOpenTripMapButton(bot *tgbotapi.BotAPI, chatID int64, cfg *config.Config, tripID string, driverUserID int64) {
	// Point to the actual HTML file served by r.Static("/webapp", "./webapp") (e.g. /webapp/index.html).
	base := strings.TrimSuffix(cfg.WebAppURL, "/")
	if base != "" && !strings.HasSuffix(base, ".html") {
		base += "/index.html"
	}
	webAppURL := base + "?trip_id=" + tripID + "&driver_id=" + strconv.FormatInt(driverUserID, 10)
	kb := webAppKeyboard{
		InlineKeyboard: [][]webAppButton{{
			{Text: "🗺️ Trip xaritasini ochish", WebApp: &webAppInfo{URL: webAppURL}},
		}},
	}
	m := tgbotapi.NewMessage(chatID, "Qabul qilindingiz ✅ Xarita ochish uchun tugmani bosing.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send Open Trip Map button: %v", err)
		// Fallback: send plain text with link
		send(bot, chatID, "Qabul qildingiz ✅ Xaritani ochish: "+webAppURL)
	}
}

func send(bot *tgbotapi.BotAPI, chatID int64, text string) {
	m := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send to %d: %v", chatID, err)
	}
}
