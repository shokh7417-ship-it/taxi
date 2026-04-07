package driver

import (
	"context"
	"database/sql"
	_ "embed"
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
	"taxi-mvp/internal/accounting"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/driverloc"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

//go:embed live_location_steps.png
var liveLocationStepsPNG []byte

const (
	btnLiveLocation     = driverloc.BtnShareLiveLocation
	btnPending          = "⏳ Тасдиқлаш кутилмоқда"
	btnRefillApplication = "📝 Аризани қайта тўлдириш"
	cbAccept             = "accept:"
	cbDriverAcceptTerms  = "driver_accept_terms"
	// cbDriverRefillApplication: after admin rejection, user taps to restart application (same as /start).
	cbDriverRefillApplication = "driver_refill_app"

	resumeDriverAccept = "driver_accept"
	// resumeDriverRelive: driver was sharing live location when legal blocked them; user must re-share live in Telegram.
	resumeDriverRelive = "driver_relive"

	// Live Location = only edited_message.location updates; active only when last_live_location_at within 90s.
	liveLocationActiveSeconds = 90
	// Onboarding: shown when driver completes registration (live location = online; no separate online button).
	onboardingMessage = "🚕 YettiQanot ҳайдовчи\n\nПастдаги «" + driverloc.BtnShareLiveLocation + "» тугмаси фақат қўлланмани кўрсатади (жонли локацияни ўзи ёқмайди). Жонли локацияни 📎 → Location → «Share Live Location» орқали уланг.\n\nЖонли локация ёқилгунча сиз офлайн ҳисобланасиз."

	// DriverApplicationRejectedTelegramText is sent when an admin rejects the application (HTTP API + bots).
	DriverApplicationRejectedTelegramText = "❌ Админ сизнинг ҳайдовчи аризангизни рад этди.\n\nКейинги қадам: қуйидаги тугмани босинг ёки /start юборинг."

	// Welcome promo message: shown once after registration (same copy as approval notifier / accounting constant).
	welcomeBonusMessage = accounting.DriverNewPromoProgramMessage
	// Bilingual instruction line for all Live Location prompts.
	liveLocationBilingualInstruction = "📎 → Геопозиция / Location → Транслировать геопозицию / Share Live Location"
	// One-time warning when Live Location becomes inactive.
	liveLocationInactiveWarningMessage = "📍 Жонли локация ўчди.\nБуюртмалар келмайди.\n\nҚайта ёқиш: " + liveLocationBilingualInstruction
	// Live is on but driver cannot receive orders (e.g. low balance); once per cooldown.
	offlineButLiveReminderMessage  = "📡 Жонли локация ёқилган.\n\nБуюртмалар олиш учун балансингиз етарли бўлиши керак. Балансни тўлдиринг."
	insufficientBalanceMessage     = "Балансингиз етарли эмас. Сўровлар олиш учун балансни тўлдиринг."
	// Registration: car types (Uzbekistan taxi market). "Бошқа" allows manual input.
	carTypeBoshqa = "Бошқа"
)

func driverAgreementInlineKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Қабул қиламан", cbDriverAcceptTerms),
		),
	)
}

// splitStringByRunes splits s into chunks of at most maxRunes runes (Telegram limit ~4096 UTF-16 code units; stay safely under).
func splitStringByRunes(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return []string{s}
	}
	var out []string
	for len(runes) > 0 {
		if len(runes) <= maxRunes {
			out = append(out, string(runes))
			break
		}
		out = append(out, string(runes[:maxRunes]))
		runes = runes[maxRunes:]
	}
	return out
}

func sendDriverAgreement(bot *tgbotapi.BotAPI, db *sql.DB, chatID int64) {
	if bot == nil {
		log.Printf("driver: sendDriverAgreement: bot is nil")
		return
	}
	ctx := context.Background()
	text, err := legal.NewService(db).DriverAgreementPromptMessage(ctx)
	if err != nil {
		log.Printf("driver: legal prompt: %v", err)
		text = legal.DriverAgreementMessage
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = legal.DriverAgreementMessage
	}
	const maxRunes = 3800
	raw := splitStringByRunes(text, maxRunes)
	var chunks []string
	for _, ch := range raw {
		if strings.TrimSpace(ch) != "" {
			chunks = append(chunks, ch)
		}
	}
	if len(chunks) == 0 {
		chunks = []string{legal.DriverAgreementMessage}
	}
	log.Printf("driver: sendDriverAgreement chat_id=%d chunks=%d", chatID, len(chunks))
	kb := driverAgreementInlineKeyboard()
	for i, chunk := range chunks {
		m := tgbotapi.NewMessage(chatID, chunk)
		if i == len(chunks)-1 {
			m.ReplyMarkup = kb
		}
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send agreement chunk %d/%d: %v", i+1, len(chunks), err)
			return
		}
	}
}

// driverLegalAllowsLiveSharing is true only when the driver has accepted active driver_terms and driver privacy policy.
// Live location must never count as "online" without this.
func driverLegalAllowsLiveSharing(ctx context.Context, db *sql.DB, userID int64) bool {
	return legal.NewService(db).DriverHasActiveLegal(ctx, userID)
}

// blockDriverLiveForMissingLegal clears live/online flags, queues legal relive, sends the latest oferta, refreshes the pin.
func blockDriverLiveForMissingLegal(ctx context.Context, bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, userID int64) {
	_, _ = db.ExecContext(ctx, `
		UPDATE drivers SET is_active = 0, live_location_active = 0, last_live_location_at = NULL, manual_offline = 0
		WHERE user_id = ?1`, userID)
	_ = legal.NewService(db).SetPendingResume(ctx, userID, resumeDriverRelive, "")

	// Live location updates can be very frequent; never spam the chat with repeated oferta prompts.
	// We gate by the active legal fingerprint stored in drivers.legal_terms_prompt_fingerprint:
	// - If we already prompted the current active bundle → stay silent on further updates.
	// - If versions change (fingerprint changes) → prompt once again.
	fp, err := legal.ActiveLegalFingerprintForTypes(ctx, db, []string{legal.DocDriverTerms, legal.DocPrivacyPolicyDriver})
	if err != nil {
		log.Printf("driver: ActiveLegalFingerprint (live block) user_id=%d: %v", userID, err)
		// Safe fallback: send once via existing gating (it will store fingerprint if possible).
		sendDriverAgreementForDriver(bot, db, chatID, userID, false, false)
		send(bot, chatID, "⚠️ Жорий шартномани қабул қилмасдан жонли локация орқали онлайн бўлиш мумкин эмас. Шартномани қабул қилинг, сўнг локацияни қайта уланг.")
		sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
		return
	}
	var stored sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT legal_terms_prompt_fingerprint FROM drivers WHERE user_id = ?1`, userID).Scan(&stored); err != nil && err != sql.ErrNoRows {
		log.Printf("driver: read legal_terms_prompt_fingerprint (live block) user_id=%d: %v", userID, err)
		stored = sql.NullString{}
	}
	st := ""
	if stored.Valid {
		st = strings.TrimSpace(stored.String)
	}
	if fp != "" && st == fp {
		// Already prompted current legal bundle; do not resend on every live location update.
		return
	}

	// First time we block for this legal bundle (or versions were bumped) → send oferta once.
	sendDriverAgreementForDriver(bot, db, chatID, userID, true, false)
	send(bot, chatID, "⚠️ Жорий шартномани қабул қилмасдан жонли локация орқали онлайн бўлиш мумкин эмас. Қуйидаги матнни ўқинг, «✅ Қабул қиламан» ни босинг, сўнг локацияни қайта уланг.")
	sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
}

func driverHasAcceptedAgreement(ctx context.Context, db *sql.DB, userID int64) bool {
	return legal.NewService(db).DriverHasActiveLegal(ctx, userID)
}

// driverWasOnlineOrLiveIntent is true when the driver had an active or recent Telegram live-location session (legal re-accept must prompt re-sharing live; is_active alone is ignored).
func driverWasOnlineOrLiveIntent(ctx context.Context, db *sql.DB, userID int64) bool {
	var liveAct int
	var lastLive sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(live_location_active, 0), last_live_location_at
		FROM drivers WHERE user_id = ?1`, userID).Scan(&liveAct, &lastLive); err != nil {
		return false
	}
	if liveAct == 1 {
		return true
	}
	if lastLive.Valid && lastLive.String != "" {
		if t, err := parseUTC(lastLive.String); err == nil {
			if time.Since(t) <= liveLocationActiveSeconds*time.Second {
				return true
			}
		}
	}
	return false
}

func resetDriverLiveOnlineStateForLegalRelive(ctx context.Context, db *sql.DB, userID int64) {
	_, _ = db.ExecContext(ctx, `
		UPDATE drivers SET is_active = 0, manual_offline = 0, live_location_active = 0, last_live_location_at = NULL
		WHERE user_id = ?1`, userID)
}

func postLegalReliveMessage(pendingRequestID string) string {
	s := "✅ Янги шартнома қабул қилинди.\n\nТизимда ҳозир офлайнсиз. Буюртмалар олиш учун Telegramда жонли локацияни қайта уланг («" + driverloc.BtnShareLiveLocation + "»)."
	if strings.TrimSpace(pendingRequestID) != "" {
		s += "\n\nБуюртмани қабул қилиш учун ҳам жонли локацияни уланг — сўров амал қилиши билан қайта уриниб кўринг."
	}
	return s
}

var (
	carTypes = []string{"Cobalt", "Nexia", "Nexia 2", "Nexia 3", "Matiz", "Gentra", "Lacetti", "Malibu", "BYD", "Lada", "Damas", carTypeBoshqa}
	colors   = []string{"Оқ", "Қора", "Сариқ", "Қизил", "Кулранг", "Бошқа"}
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
		{tgbotapi.NewKeyboardButton("Оқ"), tgbotapi.NewKeyboardButton("Қора")},
		{tgbotapi.NewKeyboardButton("Сариқ"), tgbotapi.NewKeyboardButton("Қизил")},
		{tgbotapi.NewKeyboardButton("Кулранг"), tgbotapi.NewKeyboardButton(carTypeBoshqa)},
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

// trimKeyboardNoise trims spaces and invisible/format chars some Telegram clients append to reply-button text.
func trimKeyboardNoise(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\u200e\u200f\u200b\u200c\u200d\ufeff")
	return s
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

const offlineLiveReminderCooldownMin = 60

// sendOfflineButLiveReminderIfNeeded sends a throttled reminder when live is on but the driver cannot receive orders (e.g. low balance).
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

// handleLiveLocationInstruction runs when the driver presses the reply «Jonli lokatsiyani ulashish» button (plain text)
// or after a one-shot map pin. Always sends the full illustrated guide; it does not start/stop Telegram live location.
func handleLiveLocationInstruction(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
		send(bot, chatID, "Хатолик.")
		return
	}
	var verificationStatus sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, userID).Scan(&verificationStatus)
	if !verificationStatus.Valid || strings.TrimSpace(verificationStatus.String) != "approved" {
		kb := driverKeyboardForVerificationPending()
		m := tgbotapi.NewMessage(chatID, "Тасдиқлаш кутилмоқда. Админ профилингизни текширмоқда.")
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send pending verification live-location message: %v", err)
		}
		return
	}
	kb := getDriverKeyboard(db, userID)
	if len(liveLocationStepsPNG) > 0 {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: "live_location_steps.png", Bytes: liveLocationStepsPNG})
		photo.Caption = driverloc.LiveInstruction
		photo.ReplyMarkup = kb
		if _, err := bot.Send(photo); err != nil {
			log.Printf("driver: send live location instruction photo failed: %v", err)
		} else {
			nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, userID)
			return
		}
	}
	m := tgbotapi.NewMessage(chatID, driverloc.LiveInstruction)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send live location instruction: %v", err)
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = db.ExecContext(ctx, `UPDATE drivers SET live_location_hint_last_sent_at = ?1 WHERE user_id = ?2`, nowStr, userID)
}

// Run starts the driver bot and blocks until ctx is cancelled.
// bot is the driver Telegram bot API; matchService for dispatch and pending-request push; assignmentService for Accept; tripService for START/AddPoint/FINISH (may be nil).
func Run(ctx context.Context, cfg *config.Config, db *sql.DB, bot *tgbotapi.BotAPI, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService) error {
	log.Printf("driver bot: started @%s", bot.Self.UserName)

	// Set command panel (global, Latin descriptions).
	driverCommands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Ботни бошлаш"},
		tgbotapi.BotCommand{Command: "status", Description: "Ҳолат ва баланс"},
		tgbotapi.BotCommand{Command: "bonuslar", Description: "Бонуслар ва referral статистикаси"},
		tgbotapi.BotCommand{Command: "referral", Description: "Дўстларни таклиф қилиш"},
		tgbotapi.BotCommand{Command: "leaderboard", Description: "Энг фаол ҳайдовчилар"},
		tgbotapi.BotCommand{Command: "terms", Description: "Фойдаланиш қоидалари"},
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

// driverKeyboardApprovedMain is the main reply keyboard for approved drivers: «Jonli lokatsiyani ulashish»
// opens only the instructional guide (plain text button); online/offline still follow Telegram live share.
func driverKeyboardApprovedMain() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(driverloc.ReplyKeyboardButtonShareLiveLocation()),
	)
	kb.ResizeKeyboard = true
	return kb
}

// getDriverKeyboard returns the keyboard for the driver (pending verification vs live-location help only).
func getDriverKeyboard(db *sql.DB, driverUserID int64) tgbotapi.ReplyKeyboardMarkup {
	ctx := context.Background()
	var verificationStatus sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, driverUserID).
		Scan(&verificationStatus)

	// Agar haydovchi hali tasdiqlanmagan bo'lsa, faqat "⏳ Tasdiqlash kutilmoqda" tugmasi ko'rinadi.
	if !verificationStatus.Valid || strings.TrimSpace(verificationStatus.String) != "approved" {
		return driverKeyboardForVerificationPending()
	}

	return driverKeyboardApprovedMain()
}

// KeyboardForOffline returns the standard driver reply keyboard (same as approved main: live-location help).
func KeyboardForOffline() tgbotapi.ReplyKeyboardMarkup {
	return driverKeyboardApprovedMain()
}

// SendKeyboardForDriver sends a message with the driver reply keyboard so the driver sees the live-location help button after approval.
func SendKeyboardForDriver(bot *tgbotapi.BotAPI, db *sql.DB, chatID, userID int64) {
	if bot == nil || db == nil {
		return
	}
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "Қуйидаги тугмалардан фойдаланинг:")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send keyboard for driver user_id=%d: %v", userID, err)
	}
}

// RejectionAfterAdminRefillKeyboard is the inline keyboard for the post-rejection Telegram message (admin API + bots).
func RejectionAfterAdminRefillKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btnRefillApplication, cbDriverRefillApplication),
		),
	)
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

// formatStatusPanelText returns the status panel text (same layout as /status and pinned panel).
// Holat follows Telegram live location only (not is_active alone). balance = promo_balance + cash_balance (total internal wallet for dispatch).
func formatStatusPanelText(ctx context.Context, db *sql.DB, cfg *config.Config, userID int64) (string, error) {
	var promoBal, cashBal, balance int64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(promo_balance, 0), COALESCE(cash_balance, 0), COALESCE(balance, 0)
		FROM drivers WHERE user_id = ?1`, userID).Scan(&promoBal, &cashBal, &balance)
	if err != nil {
		return "", err
	}
	liveOK := isDriverSharingLiveLocation(ctx, db, userID)
	balOK := isDriverBalanceSufficient(ctx, db, userID, cfg)

	holat := "🔴 Офлайн"
	if liveOK {
		holat = "🟢 Онлайн"
	}
	locLine := "❌ Жонли локация уланмаган"
	if liveOK {
		locLine = "✅ Жонли локация уланган"
	}
	platformCredit := promoBal

	var b strings.Builder
	fmt.Fprintf(&b, "📊 Ҳайдовчи ҳолати\n\nҲолат: %s\nЛокация: %s\n\n", holat, locLine)
	fmt.Fprintf(&b, "💰 Промо / платформа кредити: %d сўм\n(Реал пул эмас, нақдлаштирилмайди)\n\n", platformCredit)
	fmt.Fprintf(&b, "💵 Ички пул баланси (top-up): %d сўм\nЖами: %d сўм", cashBal, balance)
	if !liveOK {
		fmt.Fprintf(&b, "\n\n⚠️ Ишлаш учун жонли локацияни уланг.")
	} else if !balOK {
		fmt.Fprintf(&b, "\n\n⚠️ Буюртмалар учун баланс етарли эмас.")
	}
	return b.String(), nil
}

// sendOrUpdatePinnedStatus sends or updates the pinned status message for the driver. If a pinned message exists, edits it; otherwise sends new, stores message_id, and pins.
func sendOrUpdatePinnedStatus(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, userID int64) {
	ctx := context.Background()
	text, err := formatStatusPanelText(ctx, db, cfg, userID)
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
	// Do NOT attach reply keyboard to the pinned status message.
	// In practice this message can become non-editable when a reply keyboard is attached, causing
	// repeated "message can't be edited" errors and status spam. Keyboards are shown via other messages.
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
func UpdatePinnedStatusForChat(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, chatID).Scan(&userID); err != nil || userID == 0 {
		return
	}
	sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
}

func handleUpdate(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		handleCallback(bot, db, cfg, matchService, assignmentService, tripService, update.CallbackQuery)
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
		handleStart(bot, db, cfg, chatID, telegramID, referredBy)
		return
	case "status":
		handleStatus(bot, db, cfg, chatID, telegramID)
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
	case "terms":
		ctx := context.Background()
		_, content, err := legal.NewService(db).ActiveDocument(ctx, legal.DocDriverTerms)
		if err != nil {
			send(bot, chatID, legal.DriverAgreementMessage)
		} else {
			send(bot, chatID, content)
		}
		return
	case "privacy":
		ctx := context.Background()
		_, content, err := legal.NewService(db).ActiveDocument(ctx, legal.DocPrivacyPolicyDriver)
		if err != nil {
			send(bot, chatID, "Махфийлик сиёсати юкланмади.")
		} else {
			send(bot, chatID, content)
		}
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
	// Many users send license/vehicle images as a "file" (Document) instead of a gallery photo — same handler.
	if msg.Document != nil && strings.TrimSpace(msg.Document.FileID) != "" {
		mime := strings.ToLower(strings.TrimSpace(msg.Document.MimeType))
		if mime == "" || strings.HasPrefix(mime, "image/") {
			if handleApplicationPhoto(bot, db, cfg, chatID, telegramID, msg.Document.FileID) {
				return
			}
		}
	}

	// Handle keyboard button presses first so they always work (e.g. Live Location instruction even during registration).
	// Match both plain-text replies and labels with stray bidi/zero-width characters.
	if trimKeyboardNoise(msg.Text) == btnLiveLocation {
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

func handleStart(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, telegramID int64, referredBy *string) {
	ctx := context.Background()
	code, err := utils.GenerateReferralCode(ctx, db)
	if err != nil {
		log.Printf("driver: generate referral code: %v", err)
		send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
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
		send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
		return
	}
	// Driver referral row for accounting (reward on trip FINISH after 3 finished trips; see accounting.TryGrantReferralReward).
	var rb sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT referred_by FROM users WHERE id = ?1`, userID).Scan(&rb)
	if rb.Valid && strings.TrimSpace(rb.String) != "" {
		if err := accounting.RecordDriverReferral(ctx, db, userID, rb.String); err != nil {
			log.Printf("driver: record driver_referrals user_id=%d: %v", userID, err)
		}
	}
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
			send(bot, chatID, "Иловани тўлдиринг. Кейинги саволга жавоб юборинг.")
		}
		return
	}
	clearApplicationStep(ctx, db, userID)
	// Application is "complete" once both doc photos exist. /start does not re-run the form, so resend oferta whenever
	// the driver still owes active legal acceptances (any verification_status, incl. NULL or rejected+re-upload edge cases).
	if !driverHasAcceptedAgreement(ctx, db, userID) {
		sendDriverAgreementForDriver(bot, db, chatID, userID, false, false)
		if !driverHasAcceptedAgreement(ctx, db, userID) {
			send(bot, chatID, "⚠️ Админ тасдиқигача буюртма олиш учун шартномани қабул қилишингиз керак.")
		}
	}
	// Rewards and signup bonus are paid when docs are submitted (handleApplicationPhoto).
	var statusMsgID sql.NullInt64
	_ = db.QueryRowContext(ctx, `SELECT status_message_id FROM drivers WHERE user_id = ?1`, userID).Scan(&statusMsgID)
	// First visit after registration: one onboarding text; returning drivers only get pin refresh (no repeated wall of text).
	if !statusMsgID.Valid || statusMsgID.Int64 == 0 {
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, onboardingMessage)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send: %v", err)
		}
	}
	sendWelcomeBonusMessageIfNeeded(bot, db, chatID, userID)
	sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
	// First-time approved drivers: one live-location instruction (same rules as the reply button; skips if already sent).
	var ver sql.NullString
	var hint sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT verification_status, live_location_hint_last_sent_at FROM drivers WHERE user_id = ?1`, userID).Scan(&ver, &hint); err == nil {
		if strings.TrimSpace(ver.String) == "approved" && (!hint.Valid || strings.TrimSpace(hint.String) == "") && isDriverFullyRegistered(ctx, db, userID) {
			handleLiveLocationInstruction(bot, db, chatID, telegramID)
		}
	}
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
		text := "Иловани тўлдиринг. Телефон рақамингизни юборинг (тугмани босинг ёки ёзинг)."
		kb := tgbotapi.NewReplyKeyboard(
			tgbotapi.NewKeyboardButtonRow(
				tgbotapi.NewKeyboardButtonContact("📞 Телефон рақамини юбориш"),
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
		send(bot, chatID, "Исмингизни киритинг")
		return
	case "last_name":
		send(bot, chatID, "Фамилиянгизни киритинг")
		return
	case "car_type":
		text := "Машина турини танланг ёки «Бошқа» босинг ва ёзинг."
		kb := carTypeKeyboard()
		m := tgbotapi.NewMessage(chatID, text)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send application prompt: %v", err)
		}
		return
	case "car_type_manual":
		send(bot, chatID, "Машина моделини ёзинг.")
		return
	case "color":
		text := "Машина рангини танланг ёки «Бошқа» босинг ва ёзинг."
		kb := colorKeyboard()
		m := tgbotapi.NewMessage(chatID, text)
		m.ReplyMarkup = kb
		if _, err := bot.Send(m); err != nil {
			log.Printf("driver: send application prompt: %v", err)
		}
		return
	case "color_manual":
		send(bot, chatID, "Машина рангини ёзинг.")
		return
	case "plate":
		text := "🚘 Давлат рақамингизни тўлиқ киритинг\n\nМасалан: 20N306ZB"
		send(bot, chatID, text)
		return
	case "license_photo":
		text := "📄 Ҳайдовчилик гувоҳномаси\n\nИлтимос, ҳайдовчилик гувоҳномангиз расмини юборинг.\n\nТалаблар:\n• расм аниқ бўлсин\n• барча маълумотлар кўринсин"
		send(bot, chatID, text)
		return
	case "vehicle_doc":
		text := "🚗 Тех паспорт\n\nМашинанинг тех паспорти расмини юборинг.\n\nТалаблар:\n• расм аниқ бўлсин\n• давлат рақами кўринсин"
		send(bot, chatID, text)
		return
	default:
		_, _ = db.ExecContext(context.Background(), `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, step, driverUserID)
		send(bot, chatID, "Телефон рақамингизни юборинг.")
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

	// Require driver agreement (oferta) before sending admin approval request.
	if !driverHasAcceptedAgreement(ctx, db, userID) {
		sendDriverAgreementForDriver(bot, db, chatID, userID, true, false)
		send(bot, chatID, "⚠️ Аввал шартномани қабул қилишингиз керак.")
		return true
	}

	sendAdminApprovalRequest(ctx, bot, db, cfg, userID, telegramID)

	// Notify driver.
	send(bot, chatID, "✅ Маълумотларингиз қабул қилинди.\nАдмин тасдиқлашидан сўнг сизга хабар берилади.")
	rewardReferrerOnApplicationComplete(bot, db, userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "Тасдиқлаш кутилмоқда. Ҳолатни /status буюрғи орқали текширинг.")
	m.ReplyMarkup = kb
	_, _ = bot.Send(m)
	return true
}

func sendAdminApprovalRequest(ctx context.Context, bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, userID int64, telegramID int64) {
	var alreadySent int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(application_admin_sent, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&alreadySent)
	if alreadySent != 0 {
		log.Printf("driver: skip duplicate admin application packet user_id=%d", userID)
		return
	}
	// Load driver info for admin approval request.
	var firstName, lastName, phone, carModel, color, plateNumber sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(first_name, ''), COALESCE(last_name, ''), COALESCE(phone, ''), COALESCE(car_type, ''), COALESCE(color, ''), COALESCE(plate_number, '')
		FROM drivers WHERE user_id = ?1`, userID).Scan(&firstName, &lastName, &phone, &carModel, &color, &plateNumber); err != nil {
		log.Printf("driver: load driver info for admin approval user_id=%d: %v", userID, err)
		return
	}
	if cfg == nil || cfg.AdminID == 0 || cfg.AdminBotToken == "" {
		return
	}
	fullName := strings.TrimSpace(strings.TrimSpace(firstName.String) + " " + strings.TrimSpace(lastName.String))
	adminText := fmt.Sprintf(
		"🚕 Янги ҳайдовчи тасдиқлаш учун\n\n👤 Исм фамилия: %s\n📞 Телефон: %s\n🚗 Машина: %s\n🎨 Ранг: %s\n🔢 Рақам: %s\n👤 Telegram ID: %d\n\n📄 Ҳужжатлар қуйида",
		fullName, phone.String, carModel.String, color.String, plateNumber.String, telegramID,
	)
	adminChatID := cfg.AdminID
	adminBot, err := tgbotapi.NewBotAPI(cfg.AdminBotToken)
	if err != nil {
		log.Printf("driver: create admin bot for approval user_id=%d: %v", userID, err)
		return
	}
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
						photo := tgbotapi.NewPhoto(adminChatID, tgbotapi.FileBytes{Name: "license.jpg", Bytes: data})
						if _, err := adminBot.Send(photo); err != nil {
							log.Printf("driver: admin license photo send error user_id=%d: %v", userID, err)
						} else {
							log.Printf("driver: admin license photo sent user_id=%d", userID)
						}
					}
				}
			}
		}
		// Vehicle document
		var vehicleID sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT vehicle_doc_file_id FROM drivers WHERE user_id = ?1`, userID).Scan(&vehicleID)
		if vehicleID.Valid && vehicleID.String != "" {
			if f, err := bot.GetFile(tgbotapi.FileConfig{FileID: vehicleID.String}); err != nil {
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
						photo := tgbotapi.NewPhoto(adminChatID, tgbotapi.FileBytes{Name: "vehicle_doc.jpg", Bytes: data})
						if _, err := adminBot.Send(photo); err != nil {
							log.Printf("driver: admin vehicle doc photo send error user_id=%d: %v", userID, err)
						} else {
							log.Printf("driver: admin vehicle doc photo sent user_id=%d", userID)
						}
					}
				}
			}
		}
	}

	// Instruction text + inline buttons via admin bot (callbacks handled by admin bot).
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Тасдиқлаш", fmt.Sprintf("approve_driver_%d", userID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Рад этиш", fmt.Sprintf("reject_driver_%d", userID)),
		),
	)
	inlineMsg := tgbotapi.NewMessage(adminChatID, "Ҳайдовчини тасдиқланг ёки рад этинг.")
	inlineMsg.ReplyMarkup = kb
	if _, err := adminBot.Send(inlineMsg); err != nil {
		log.Printf("driver: admin approval inline buttons send error user_id=%d: %v", userID, err)
	} else {
		log.Printf("driver: admin approval buttons sent via admin bot user_id=%d", userID)
		if _, err := db.ExecContext(ctx, `UPDATE drivers SET application_admin_sent = 1 WHERE user_id = ?1`, userID); err != nil {
			log.Printf("driver: set application_admin_sent user_id=%d: %v", userID, err)
		}
	}
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
			send(bot, chatID, "Бу телефон рақами аллақачон рўйхатдан ўтган. Бошқа рақамдан фойдаланинг.")
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
			send(bot, chatID, "Машина моделини ёзинг.")
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
		if text == carTypeBoshqa {
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET application_step = ?1 WHERE user_id = ?2`, "color_manual", userID)
			send(bot, chatID, "Машина рангини ёзинг.")
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
			send(bot, chatID, "❌ Нотўғри рақам формати.\n\nТўғри формат: 20N306ZB\nИлтимос, давлат рақамини тўлиқ киритинг.")
			return true
		}
		// Enforce unique plate: if another driver already registered this plate, block and ask for a different one.
		var existingUserID int64
		if err := db.QueryRowContext(ctx, `
			SELECT user_id FROM drivers
			WHERE (COALESCE(plate_number,'') = ?1 OR COALESCE(plate,'') = ?1) AND user_id != ?2
			LIMIT 1`,
			plate, userID).Scan(&existingUserID); err == nil && existingUserID != 0 {
			send(bot, chatID, "❌ Бу рақам аллақачон рўйхатдан ўтган.\n\nИлтимос, бошқа давлат рақамини киритинг.")
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

// rewardReferrerOnApplicationComplete notifies the referrer when a referred driver completes registration (docs submitted).
// It does NOT add any balance; referral reward is granted in TripService.FinishTrip after 3 FINISHED trips (promo + ledger).
func rewardReferrerOnApplicationComplete(bot *tgbotapi.BotAPI, db *sql.DB, newDriverUserID int64) {
	ctx := context.Background()
	var referredBy sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT referred_by FROM users WHERE id = ?1`, newDriverUserID).Scan(&referredBy)
	if referredBy.Valid && strings.TrimSpace(referredBy.String) != "" {
		if err := accounting.RecordDriverReferral(ctx, db, newDriverUserID, referredBy.String); err != nil {
			log.Printf("driver: record driver_referrals user_id=%d: %v", newDriverUserID, err)
		}
	}
	var stage1Paid int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(referral_stage1_reward_paid, 0) FROM users WHERE id = ?1`, newDriverUserID).Scan(&stage1Paid); err != nil || !referredBy.Valid || referredBy.String == "" || stage1Paid != 0 {
		return
	}
	var referrerUserID, referrerTelegramID int64
	if err := db.QueryRowContext(ctx, `SELECT id, telegram_id FROM users WHERE referral_code = ?1`, referredBy.String).Scan(&referrerUserID, &referrerTelegramID); err != nil || referrerUserID == 0 {
		return
	}
	// Mark this referred user as stage1 processed (notification sent).
	_, _ = db.ExecContext(ctx, `UPDATE users SET referral_stage1_reward_paid = 1 WHERE id = ?1`, newDriverUserID)
	var newDriverName string
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(TRIM(name), ''), 'Янги ҳайдовчи') FROM users WHERE id = ?1`, newDriverUserID).Scan(&newDriverName)
	if referrerTelegramID != 0 && bot != nil {
		msg := fmt.Sprintf("🎉 Дўстингиз %s таклиф ҳаволангиз орқали ҳайдовчи бўлиб рўйхатдан ўтди.\n\nУ 3 та сафарни якунлагач сизга\n20 000 промо кредит берилади (реал пул эмас).", newDriverName)
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
		send(bot, chatID, "Аввал /start босинг.")
		return
	}
	code := referralCode.String
	if !referralCode.Valid || code == "" {
		var err error
		code, err = utils.GenerateReferralCode(ctx, db)
		if err != nil {
			log.Printf("driver: generate referral code for /referral: %v", err)
			send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
			return
		}
		if _, err := db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, userID); err != nil {
			log.Printf("driver: save referral code: %v", err)
			send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
			return
		}
	}
	botUsername := ""
	if bot != nil {
		botUsername = bot.Self.UserName
	}
	shareLink := utils.ReferralLink(botUsername, code)
	text := "🎁 Ҳайдовчи таклиф қилинг\n\nҲар бир таклиф қилинган ҳайдовчи\n3 та сафарни якунлагач\nсизга +20 000 промо кредит\n(реал пул эмас, нақдлаштирилмайди)\n\nСизнинг referral ҳаволангиз:\n" + shareLink
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
		send(bot, chatID, "Аввал /start босинг.")
		return
	}
	code := referralCode.String
	if !referralCode.Valid || code == "" {
		var err error
		code, err = utils.GenerateReferralCode(ctx, db)
		if err != nil {
			log.Printf("driver: generate referral code for /bonuslar: %v", err)
			send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
			return
		}
		if _, err := db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, userID); err != nil {
			log.Printf("driver: save referral code: %v", err)
			send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
			return
		}
	}
	var referredCount int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u INNER JOIN drivers d ON d.user_id = u.id WHERE u.referred_by = ?1`, code).Scan(&referredCount)
	var stage1Count int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referred_by = ?1 AND COALESCE(referral_stage1_reward_paid, 0) = 1`, code).Scan(&stage1Count)
	var stage2Count int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE referred_by = ?1 AND COALESCE(referral_stage2_reward_paid, 0) = 1`, code).Scan(&stage2Count)
	// stage2_count = referred drivers for whom inviter referral reward was granted (3 finished trips).
	totalEarnings := stage2Count * accounting.ReferralRewardPromoSoM
	text := fmt.Sprintf("📊 Referral статистикаси\n\nТаклиф қилган ҳайдовчилар: %d\nПромо referral жами: %d сўм", referredCount, totalEarnings)
	text += "\n\n🔗 Таклиф ҳаволаси: /referral"
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
		SELECT COALESCE(NULLIF(TRIM(u.name), ''), 'Ҳайдовчи') AS name,
		       (SELECT COUNT(*) FROM users u2 INNER JOIN drivers d2 ON d2.user_id = u2.id WHERE u2.referred_by = u.referral_code) AS cnt
		FROM users u
		INNER JOIN drivers d ON d.user_id = u.id
		WHERE u.referral_code IS NOT NULL AND u.referral_code != ''
		ORDER BY cnt DESC
		LIMIT 10`)
	if err != nil {
		log.Printf("driver: leaderboard query: %v", err)
		send(bot, chatID, "Хатолик. Қайта уриниб кўринг.")
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
		lines = append(lines, fmt.Sprintf("%d. %s — %d та ҳайдовчи", rank, name, cnt))
		rank++
	}
	if err := rows.Err(); err != nil {
		log.Printf("driver: leaderboard rows: %v", err)
	}
	text := "🏆 Энг фаол ҳайдовчилар\n\n"
	if len(lines) == 0 {
		text += "Ҳали маълумот йўқ."
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

// handleStatus updates the pinned status card only (edit-first; avoids duplicate status messages in chat).
func handleStatus(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
		send(bot, chatID, "Аввал /start босинг.")
		return
	}
	sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
}

func handleRequestLocation(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	ctx := context.Background()
	var userID int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	kb := getDriverKeyboard(db, userID)
	m := tgbotapi.NewMessage(chatID, "Локацияни Telegramда 📎 → Location орқали юборинг.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send: %v", err)
	}
}

// handleLiveLocationUpdate processes edited_message.location (live update or live end) or message.location with live_period (live start).
// If loc.LivePeriod <= 0 when from edited_message, treats as live end: clears live state and sets driver offline (is_active=0).
func handleLiveLocationUpdate(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, tripService *services.TripService, chatID, telegramID int64, loc *tgbotapi.Location, updateTime time.Time) {
	ctx := context.Background()
	// Live end: edited_message with location.live_period null/0 — stop accepting updates and clear live state; send one-time warning.
	if loc != nil && loc.LivePeriod <= 0 {
		var userID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil {
			return
		}
		if !driverLegalAllowsLiveSharing(ctx, db, userID) {
			blockDriverLiveForMissingLegal(ctx, bot, db, cfg, chatID, userID)
			return
		}
		_, _ = db.ExecContext(ctx, `
			UPDATE drivers SET is_active = 0, manual_offline = 0, live_location_active = 0, last_live_location_at = NULL
			WHERE user_id = ?1`, userID)
		log.Printf("driver: live_location end user_id=%d", userID)
		sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, liveLocationInactiveWarningMessage)
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
	if !driverLegalAllowsLiveSharing(ctx, db, userID) {
		blockDriverLiveForMissingLegal(ctx, bot, db, cfg, chatID, userID)
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
	var lastLiveAt sql.NullString
	var prevLiveActive int
	_ = db.QueryRowContext(ctx, `
		SELECT last_lat, last_lng, last_live_location_at, COALESCE(live_location_active, 0)
		FROM drivers WHERE user_id = ?1`,
		userID).Scan(&prevLat, &prevLng, &lastLiveAt, &prevLiveActive)
	// Staleness must use last_live_location_at, not last_seen_at: POST /driver/location (Mini App)
	// updates last_seen_at without touching last_live_location_at, which would make every Telegram
	// edit look "older" than DB and skip coordinate updates / trip_point incorrectly.
	stale := false
	if lastLiveAt.Valid && lastLiveAt.String != "" {
		if parsed, err := parseUTC(lastLiveAt.String); err == nil && !updateTime.After(parsed) {
			log.Printf("driver: live_location ignored stale user_id=%d", userID)
			stale = true
		}
	}
	// Pin gating:
	// Telegram may update live coordinates with gaps > 90s. We only want to pin once per live session,
	// therefore we use `drivers.live_location_active` as the state source (reset only when stop sharing).
	wasLiveActive := prevLiveActive == 1

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
	// Update pinned panel when live session starts (no extra chat spam; card shows online).
	if !wasLiveActive {
		sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
	}

	// If driver has STARTED trip, add point (no chat message)
	var startedTripID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM trips WHERE driver_user_id = ?1 AND status = ?2 LIMIT 1`, userID, domain.TripStatusStarted).Scan(&startedTripID); err == nil && startedTripID != "" && tripService != nil {
		_, _, _ = tripService.AddPoint(ctx, startedTripID, userID, lat, lng, time.Now())
		log.Printf("driver: live_location trip_point user_id=%d trip_id=%s lat=%.6f lng=%.6f", userID, startedTripID, lat, lng)
		return
	}
	// If assigned but trip not started yet (WAITING or ARRIVED), do not treat as "no trip" for auto-online / dispatch.
	var preStartTripID string
	var preStartStatus string
	if err := db.QueryRowContext(ctx, `SELECT id, status FROM trips WHERE driver_user_id = ?1 AND status IN (?2, ?3) LIMIT 1`, userID, domain.TripStatusWaiting, domain.TripStatusArrived).Scan(&preStartTripID, &preStartStatus); err == nil && preStartTripID != "" {
		log.Printf("driver: live_location skip_dispatch user_id=%d trip_status=%s trip_id=%s", userID, preStartStatus, preStartTripID)
		return
	}

	// No active trip: sharing live location means eligible drivers go online (balance + legal).
	if isDriverBalanceSufficient(ctx, db, userID, cfg) && driverHasAcceptedAgreement(ctx, db, userID) {
		onlineNowStr := updateTime.UTC().Format("2006-01-02 15:04:05")
		if stale {
			onlineNowStr = time.Now().UTC().Format("2006-01-02 15:04:05")
		}
		var prevActive int
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(is_active, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&prevActive)
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET is_active = 1, manual_offline = 0, last_seen_at = ?1 WHERE user_id = ?2`,
			onlineNowStr, userID)
		if prevActive == 0 && wasLiveActive {
			sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
		}
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
		var wasActive int
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(is_active, 0) FROM drivers WHERE user_id = ?1`, userID).Scan(&wasActive)
		_, _ = db.ExecContext(ctx, `UPDATE drivers SET is_active = 0, manual_offline = 0 WHERE user_id = ?1`, userID)
		if wasActive == 1 {
			sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
		}
		sendOfflineButLiveReminderIfNeeded(bot, db, chatID, userID)
	}
}

// handleLocation processes message.location. Only Telegram Live Location is accepted; static location is rejected.
func handleLocation(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, tripService *services.TripService, chatID, telegramID int64, loc *tgbotapi.Location, silent bool, updateTime time.Time) {
	if loc == nil {
		return
	}
	// One-shot / static location (live_period == 0): from the reply “request location” button or a map pin.
	// Dispatch requires Telegram *live* location; show the same guided flow as the text button (throttled inside).
	if loc.LivePeriod <= 0 {
		if !silent {
			handleLiveLocationInstruction(bot, db, chatID, telegramID)
		}
		return
	}
	// Live location start: live_period set — same handling as live update.
	handleLiveLocationUpdate(bot, db, cfg, matchService, tripService, chatID, telegramID, loc, updateTime)
}

func handleCallback(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, assignmentService *services.AssignmentService, tripService *services.TripService, q *tgbotapi.CallbackQuery) {
	chatID := q.Message.Chat.ID
	telegramID := q.From.ID
	data := q.Data

	if data == cbDriverRefillApplication {
		if q.ID != "" {
			_, _ = bot.Request(tgbotapi.NewCallback(q.ID, ""))
		}
		handleStart(bot, db, cfg, chatID, telegramID, nil)
		return
	}

	if data == cbDriverAcceptTerms {
		ctx := context.Background()
		var userID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID); err != nil || userID == 0 {
			return
		}
		lSvc := legal.NewService(db)
		before := lSvc.DriverHasActiveLegal(ctx, userID)
		if err := lSvc.AcceptActiveForTypes(ctx, userID, []string{legal.DocDriverTerms, legal.DocPrivacyPolicyDriver}, "", "telegram-bot"); err != nil {
			log.Printf("driver: legal accept user_id=%d: %v", userID, err)
			_, _ = bot.Request(tgbotapi.NewCallback(q.ID, ""))
			send(bot, chatID, "Хатолик. Кейинроқ уриниб кўринг.")
			return
		}
		if err := legal.SyncDriverLegalPromptFingerprint(ctx, db, userID); err != nil {
			log.Printf("driver: SyncDriverLegalPromptFingerprint user_id=%d: %v", userID, err)
		}
		_, _ = bot.Request(tgbotapi.NewCallback(q.ID, ""))

		reaccepted := !before
		var verificationStatus sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT verification_status FROM drivers WHERE user_id = ?1`, userID).Scan(&verificationStatus)
		stStr := strings.TrimSpace(verificationStatus.String)

		kind, payload, ok := lSvc.TakePendingResume(ctx, userID)
		if ok {
			switch kind {
			case resumeDriverRelive:
				// Live was blocked until latest terms: stay offline until Telegram live is shared again.
				resetDriverLiveOnlineStateForLegalRelive(ctx, db, userID)
				sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
				send(bot, chatID, postLegalReliveMessage(payload))
				if stStr == "approved" {
					kb := getDriverKeyboard(db, userID)
					m := tgbotapi.NewMessage(chatID, "📍 Жонли локацияни қайта уланг — тизим сизни онлайн деб ҳисоблайди.")
					m.ReplyMarkup = kb
					_, _ = bot.Send(m)
				} else if stStr == "pending_approval" && reaccepted {
					kb := getDriverKeyboard(db, userID)
					m := tgbotapi.NewMessage(chatID, "Тасдиқлаш кутилмоқда. Ҳолатни /status буюрғи орқали текширинг.")
					m.ReplyMarkup = kb
					_, _ = bot.Send(m)
					sendAdminApprovalRequest(ctx, bot, db, cfg, userID, telegramID)
				} else if stStr == "pending_approval" {
					kb := getDriverKeyboard(db, userID)
					m := tgbotapi.NewMessage(chatID, "Тасдиқлаш кутилмоқда. Ҳолатни /status буюрғи орқали текширинг.")
					m.ReplyMarkup = kb
					_, _ = bot.Send(m)
				}
				return
			case resumeDriverAccept:
				rid := strings.TrimSpace(payload)
				if rid != "" && assignmentService != nil {
					handleAccept(bot, db, cfg, assignmentService, tripService, chatID, telegramID, rid, q)
				}
				return
			}
		}

		// Re-accepted latest terms without a queued relive (e.g. /start or periodic notifier): still offline until live is re-shared.
		if reaccepted && stStr == "approved" {
			resetDriverLiveOnlineStateForLegalRelive(ctx, db, userID)
			sendOrUpdatePinnedStatus(bot, db, cfg, chatID, userID)
			send(bot, chatID, postLegalReliveMessage(""))
			kb := getDriverKeyboard(db, userID)
			m := tgbotapi.NewMessage(chatID, "📍 Жонли локацияни қайта уланг — буюртмалар фақат шундан кейин келади.")
			m.ReplyMarkup = kb
			_, _ = bot.Send(m)
			return
		}

		if before {
			if stStr == "pending_approval" {
				sendAdminApprovalRequest(ctx, bot, db, cfg, userID, telegramID)
				send(bot, chatID, "✅ Шартнома қабул қилинган.\n\nАризангиз админга юборилди.")
			} else {
				send(bot, chatID, "✅ Шартнома қабул қилинган.")
			}
			return
		}
		send(bot, chatID, "✅ Шартнома қабул қилинди.\n\nМаълумотларингиз админ томонидан текширилади.")
		kb := getDriverKeyboard(db, userID)
		m := tgbotapi.NewMessage(chatID, "Тасдиқлаш кутилмоқда. Ҳолатни /status буюрғи орқали текширинг.")
		m.ReplyMarkup = kb
		_, _ = bot.Send(m)
		sendAdminApprovalRequest(ctx, bot, db, cfg, userID, telegramID)
		return
	}

	if strings.HasPrefix(data, cbAccept) {
		requestID := strings.TrimPrefix(data, cbAccept)
		handleAccept(bot, db, cfg, assignmentService, tripService, chatID, telegramID, requestID, q)
	} else if strings.HasPrefix(data, "approve_driver_") || strings.HasPrefix(data, "reject_driver_") {
		if cfg == nil || telegramID != cfg.AdminID {
			if q.ID != "" {
				_, _ = bot.Request(tgbotapi.NewCallback(q.ID, "Рухсат йўқ"))
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
			// Startup promotional platform credit (not withdrawable cash); ledger PROMO_GRANTED.
			if err := accounting.TryGrantSignupPromoOnce(ctx, db, driverUserID); err != nil {
				log.Printf("driver: signup promo grant user_id=%d: %v", driverUserID, err)
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
			msg := tgbotapi.NewMessage(driverTgID, "🎉 Профилингиз тасдиқланди.\n\nБуюртмалар олиш учун Telegramда жонли локацияни уланг — бошқа «онлайн» тугмаси йўқ; улашганингизгача сиз офлайн.")
			if _, err := bot.Send(msg); err != nil {
				log.Printf("driver: notify approved driver send error user_id=%d: %v", driverUserID, err)
				return
			}
			sendWelcomeBonusMessageIfNeeded(bot, db, driverTgID, driverUserID)
			_, _ = db.ExecContext(ctx, `UPDATE drivers SET approval_notified = 1 WHERE user_id = ?1`, driverUserID)
		case strings.HasPrefix(data, "reject_driver_"):
			adminRepo := repositories.NewAdminDriverRepository(db)
			if err := adminRepo.DeleteAndResetDriverApplication(ctx, driverUserID); err != nil {
				log.Printf("driver: reject driver delete/reset user_id=%d: %v", driverUserID, err)
			} else {
				log.Printf("driver: driver application rejected and reset user_id=%d", driverUserID)
				var driverTgID int64
				if err := db.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = ?1`, driverUserID).Scan(&driverTgID); err == nil && driverTgID != 0 {
					msg := tgbotapi.NewMessage(driverTgID, DriverApplicationRejectedTelegramText)
					msg.ReplyMarkup = RejectionAfterAdminRefillKeyboard()
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
		send(bot, chatID, "Хатолик.")
		return
	}
	if !legal.NewService(db).DriverHasActiveLegal(ctx, userID) {
		lSvc := legal.NewService(db)
		if driverWasOnlineOrLiveIntent(ctx, db, userID) {
			_ = lSvc.SetPendingResume(ctx, userID, resumeDriverRelive, requestID)
		} else {
			_ = lSvc.SetPendingResume(ctx, userID, resumeDriverAccept, requestID)
		}
		sendDriverAgreementForDriver(bot, db, chatID, userID, true, false)
		send(bot, chatID, "⚠️ Буюртмани қабул қилиш учун аввал барча ҳужжатларни қабул қилинг.")
		return
	}
	if assignmentService == nil {
		send(bot, chatID, "Хатолик.")
		return
	}
	assigned, tripID, err := assignmentService.TryAssign(ctx, requestID, userID)
	if err != nil {
		log.Printf("driver: TryAssign: %v", err)
		send(bot, chatID, "Хатолик.")
		return
	}
	if !assigned {
		send(bot, chatID, "Сўров аллақачон бошқаға берилган ёки бекор қилинган.")
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
	Text   string      `json:"text"`
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
			{Text: "🗺️ Сафар харитасини очиш", WebApp: &webAppInfo{URL: webAppURL}},
		}},
	}
	m := tgbotapi.NewMessage(chatID, "Қабул қилдингиз ✅ Харита очиш учун тугмани босинг.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send Open Trip Map button: %v", err)
		// Fallback: send plain text with link
		send(bot, chatID, "Қабул қилдингиз ✅ Харитани очиш: "+webAppURL)
	}
}

func send(bot *tgbotapi.BotAPI, chatID int64, text string) {
	m := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(m); err != nil {
		log.Printf("driver: send to %d: %v", chatID, err)
	}
}
