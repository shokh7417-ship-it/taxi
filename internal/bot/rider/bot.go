package rider

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/legal"
	"taxi-mvp/internal/services"
	"taxi-mvp/internal/utils"
)

const (
	btnLocation    = "📍 Lokatsiya yuborish"
	btnCancel      = "❌ Bekor qilish"
	btnTaxiCall    = "🚕 Taxi chaqirish"
	btnTaxiNew     = "🚕 Yangi taxi chaqirish"
	btnHelp        = "ℹ️ Yordam"
	btnTrackDriver = "📍 Haydovchini kuzatish"
	cbRiderAcceptTerms = "rider_accept_terms"
)

// Run starts the rider bot and blocks until ctx is cancelled.
// bot is the rider Telegram bot API; matchService broadcasts new requests (may be nil); tripService is used to cancel trips (may be nil).
func Run(ctx context.Context, cfg *config.Config, db *sql.DB, bot *tgbotapi.BotAPI, matchService *services.MatchService, tripService *services.TripService) error {
	log.Printf("rider bot: started @%s", bot.Self.UserName)
	setBotCommands(bot)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	notified := &notifiedState{}
	go pollAndNotifyRider(ctx, bot, db, cfg, notified)

	for {
		select {
		case <-ctx.Done():
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			handleUpdate(bot, db, cfg, matchService, tripService, update, notified)
		}
	}
}

type notifiedState struct {
	mu       sync.Mutex
	assigned map[string]struct{}
	started  map[string]struct{}
	finished map[string]struct{}
}

func (n *notifiedState) markAssigned(requestID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.assigned[requestID]; ok {
		return false
	}
	if n.assigned == nil {
		n.assigned = make(map[string]struct{})
	}
	n.assigned[requestID] = struct{}{}
	return true
}

func (n *notifiedState) markStarted(tripID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.started[tripID]; ok {
		return false
	}
	if n.started == nil {
		n.started = make(map[string]struct{})
	}
	n.started[tripID] = struct{}{}
	return true
}

func (n *notifiedState) markFinished(tripID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.finished[tripID]; ok {
		return false
	}
	if n.finished == nil {
		n.finished = make(map[string]struct{})
	}
	n.finished[tripID] = struct{}{}
	return true
}

func handleUpdate(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, tripService *services.TripService, update tgbotapi.Update, notified *notifiedState) {
	if update.CallbackQuery != nil {
		handleCallback(bot, db, cfg, matchService, update.CallbackQuery)
		return
	}
	if update.Message == nil {
		return
	}
	msg := update.Message
	chatID := msg.Chat.ID
	telegramID := msg.From.ID

	if msg.Command() == "start" {
		var referredBy *string
		if parts := strings.Fields(msg.Text); len(parts) > 1 && parts[1] != "" {
			if code := strings.TrimPrefix(parts[1], "ref_"); code != "" {
				referredBy = &code
			}
		}
		handleStart(bot, db, chatID, telegramID, referredBy)
		return
	}
	if msg.Command() == "terms" {
		send(bot, chatID, legal.TermsFullMessage)
		return
	}
	if msg.Command() == "cancel" {
		handleCancel(bot, db, cfg, tripService, chatID, telegramID)
		return
	}

	// Block usage until rider accepts agreement.
	if !isRiderTermsAccepted(context.Background(), db, telegramID) {
		send(bot, chatID, "⚠️ Davom etish uchun avval qoidalarni qabul qilishingiz kerak.\n\n/start buyrug'ini bosing.")
		return
	}

	// Always require phone number before allowing order creation.
	if msg.Contact != nil {
		handlePhoneContact(bot, db, chatID, telegramID, msg.Contact.PhoneNumber)
		return
	}

	if msg.Location != nil {
		handleLocation(bot, db, cfg, matchService, chatID, telegramID, msg.Location.Latitude, msg.Location.Longitude)
		return
	}

	if msg.Text == btnCancel {
		handleCancel(bot, db, cfg, tripService, chatID, telegramID)
		return
	}
	if msg.Text == btnTaxiCall || msg.Text == btnTaxiNew {
		handleTaxiCall(bot, db, chatID, telegramID)
		return
	}
	if msg.Text == btnHelp {
		handleHelp(bot, chatID)
		return
	}
	if msg.Text == btnTrackDriver {
		handleTrackDriver(bot, db, cfg, chatID, telegramID)
		return
	}
}

func setBotCommands(bot *tgbotapi.BotAPI) {
	cmd := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Bosh menyu"},
		tgbotapi.BotCommand{Command: "cancel", Description: "Bekor qilish"},
		tgbotapi.BotCommand{Command: "terms", Description: "Foydalanish qoidalari"},
	)
	if _, err := bot.Request(cmd); err != nil {
		log.Printf("rider bot: SetMyCommands: %v", err)
	}
}

func handleCallback(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, q *tgbotapi.CallbackQuery) {
	// Always ACK callback quickly.
	_, _ = bot.Request(tgbotapi.NewCallback(q.ID, ""))

	if q.Data == cbRiderAcceptTerms {
		ctx := context.Background()
		telegramID := q.From.ID
		// Ensure user exists (idempotent).
		_, _ = db.ExecContext(ctx, `
			INSERT INTO users (telegram_id, role) VALUES (?1, ?2)
			ON CONFLICT (telegram_id) DO UPDATE SET role = excluded.role`,
			telegramID, domain.RoleRider)
		_, _ = db.ExecContext(ctx, `UPDATE users SET terms_accepted = 1 WHERE telegram_id = ?1`, telegramID)
		send(bot, q.Message.Chat.ID, "✅ Qoidalar qabul qilindi.\n\nEndi siz bemalol buyurtma berishingiz mumkin.")
		// Continue normal flow: ensure phone, then show menu.
		if ensureRiderPhone(bot, db, q.Message.Chat.ID, telegramID) {
			return
		}
		sendMainMenu(bot, q.Message.Chat.ID)
		return
	}

	if q.Data == "search_again" {
		telegramID := q.From.ID
		if !isRiderTermsAccepted(context.Background(), db, telegramID) {
			sendRiderAgreement(bot, q.Message.Chat.ID)
			return
		}
		// Ask phone first (always), then location.
		if ensureRiderPhone(bot, db, q.Message.Chat.ID, telegramID) {
			return
		}
		sendLocationPrompt(bot, q.Message.Chat.ID)
		return
	}
	_ = cfg
	_ = matchService
}

func handleStart(bot *tgbotapi.BotAPI, db *sql.DB, chatID int64, telegramID int64, referredBy *string) {
	ctx := context.Background()
	code, err := utils.GenerateReferralCode(ctx, db)
	if err != nil {
		log.Printf("rider: generate referral code: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
		return
	}
	var refArg interface{}
	bonusBalance := int64(0)
	if referredBy != nil && *referredBy != "" {
		refArg = *referredBy
		bonusBalance = 20000 // Rider referral bonus: 20000 so'm, fare discount only (non-withdrawable).
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO users (telegram_id, role, referral_code, referred_by, referral_bonus_balance) VALUES (?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (telegram_id) DO UPDATE SET role = excluded.role`,
		telegramID, domain.RoleRider, code, refArg, bonusBalance)
	if err != nil {
		log.Printf("rider: upsert user: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko‘ring.")
		return
	}

	if ensureRiderPhone(bot, db, chatID, telegramID) {
		return
	}
	// Show rider agreement once on first start, before allowing usage.
	if !isRiderTermsAccepted(ctx, db, telegramID) {
		sendRiderAgreement(bot, chatID)
		return
	}
	sendMainMenu(bot, chatID)
}

func isRiderTermsAccepted(ctx context.Context, db *sql.DB, telegramID int64) bool {
	var accepted int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(terms_accepted, 0) FROM users WHERE telegram_id = ?1`, telegramID).Scan(&accepted); err != nil {
		return false
	}
	return accepted != 0
}

func sendRiderAgreement(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Qabul qilaman", cbRiderAcceptTerms),
		),
	)
	m := tgbotapi.NewMessage(chatID, legal.RiderAgreementMessage)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send agreement: %v", err)
	}
}

// sendMainMenu shows the persistent main menu: Taxi chaqirish, Yordam.
func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnTaxiCall),
			tgbotapi.NewKeyboardButton(btnHelp),
		),
	)
	kb.ResizeKeyboard = true
	m := tgbotapi.NewMessage(chatID, "Quyidagi tugmalardan foydalaning:")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send main menu: %v", err)
	}
}

// SendMainMenuAfterFinish shows the post-trip menu: Yangi taxi chaqirish, Yordam (used by TripService).
func SendMainMenuAfterFinish(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnTaxiNew),
			tgbotapi.NewKeyboardButton(btnHelp),
		),
	)
	kb.ResizeKeyboard = true
	m := tgbotapi.NewMessage(chatID, "Safar tugadi. Yangi taxi chaqirish uchun tugmani bosing.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send main menu after finish: %v", err)
	}
}

func handleTaxiCall(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) {
	if ensureRiderPhone(bot, db, chatID, telegramID) {
		return
	}
	sendLocationPrompt(bot, chatID)
}

func handleHelp(bot *tgbotapi.BotAPI, chatID int64) {
	text := "Yordam:\n\n" +
		"• Taxi chaqirish — lokatsiyangizni yuboring, haydovchi topiladi.\n" +
		"• Haydovchini kuzatish — safar davomida xaritada kuzating.\n" +
		"• Bekor qilish — so'rovni yoki safarni bekor qilish.\n\n" +
		"/start — bosh menyu\n/cancel — bekor qilish"
	kb := mainMenuReplyKeyboard()
	m := tgbotapi.NewMessage(chatID, text)
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send help: %v", err)
	}
}

func mainMenuReplyKeyboard() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnTaxiCall),
			tgbotapi.NewKeyboardButton(btnHelp),
		),
	)
	kb.ResizeKeyboard = true
	return kb
}

func handleTrackDriver(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, chatID, telegramID int64) {
	var userID int64
	err := db.QueryRowContext(context.Background(), `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil || userID == 0 {
		send(bot, chatID, "Avval /start bosing.")
		return
	}
	var tripID string
	err = db.QueryRowContext(context.Background(), `
		SELECT id FROM trips
		WHERE rider_user_id = ?1 AND status IN (?2, ?3)
		ORDER BY id DESC LIMIT 1`,
		userID, domain.TripStatusWaiting, domain.TripStatusStarted).Scan(&tripID)
	if err != nil || tripID == "" {
		send(bot, chatID, "Aktiv safar topilmadi.")
		return
	}
	if cfg == nil || cfg.RiderMapURL == "" {
		send(bot, chatID, "Xarita hozircha mavjud emas.")
		return
	}
	url := strings.TrimSuffix(cfg.RiderMapURL, "/") + "?trip_id=" + tripID
	kb := riderMapWebAppKeyboard("📍 Xaritada kuzatish", url)
	m := tgbotapi.NewMessage(chatID, "Haydovchini xaritada kuzatish uchun tugmani bosing:")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send track driver: %v", err)
	}
}

// riderMapWebAppKeyboard returns an inline keyboard with one Web App button (for rider map).
func riderMapWebAppKeyboard(buttonText, webAppURL string) riderMapInlineKbd {
	return riderMapInlineKbd{
		InlineKeyboard: [][]riderMapWebAppBtn{{
			{Text: buttonText, WebApp: &riderMapWebAppInfo{URL: webAppURL}},
		}},
	}
}

type riderMapInlineKbd struct {
	InlineKeyboard [][]riderMapWebAppBtn `json:"inline_keyboard"`
}
type riderMapWebAppBtn struct {
	Text   string             `json:"text"`
	WebApp *riderMapWebAppInfo `json:"web_app,omitempty"`
}
type riderMapWebAppInfo struct {
	URL string `json:"url"`
}

// ensureRiderPhone checks if rider phone exists; if not, prompts to share contact.
// Returns true if we prompted (i.e. phone is missing).
func ensureRiderPhone(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64) bool {
	var phone sql.NullString
	_ = db.QueryRowContext(context.Background(), `SELECT phone FROM users WHERE telegram_id = ?1`, telegramID).Scan(&phone)
	if phone.Valid && phone.String != "" {
		return false
	}
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButtonContact("📞 Telefon raqamini yuborish"),
		),
	)
	kb.ResizeKeyboard = true
	kb.OneTimeKeyboard = true
	m := tgbotapi.NewMessage(chatID, "Buyurtma berish uchun telefon raqamingiz kerak. Tugmani bosib raqamingizni yuboring.")
	m.ReplyMarkup = kb
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send phone prompt: %v", err)
	}
	return true
}

func handlePhoneContact(bot *tgbotapi.BotAPI, db *sql.DB, chatID, telegramID int64, phone string) {
	if phone == "" {
		_ = ensureRiderPhone(bot, db, chatID, telegramID)
		return
	}
	_, err := db.ExecContext(context.Background(), `UPDATE users SET phone = ?1 WHERE telegram_id = ?2`, phone, telegramID)
	if err != nil {
		log.Printf("rider: save phone: %v", err)
	}
	send(bot, chatID, "Rahmat ✅ Endi menyudan «Taxi chaqirish» ni bosing.")
	sendMainMenu(bot, chatID)
}

func sendLocationPrompt(bot *tgbotapi.BotAPI, chatID int64) {
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButtonLocation(btnLocation),
		),
	)
	keyboard.ResizeKeyboard = true
	keyboard.OneTimeKeyboard = true
	m := tgbotapi.NewMessage(chatID, "Lokatsiyangizni yuboring.")
	m.ReplyMarkup = keyboard
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send: %v", err)
	}
}

func handleLocation(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, matchService *services.MatchService, chatID, telegramID int64, lat, lng float64) {
	if ensureRiderPhone(bot, db, chatID, telegramID) {
		return
	}
	var userID int64
	err := db.QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			send(bot, chatID, "Avval /start bosing.")
			return
		}
		log.Printf("rider: get user: %v", err)
		send(bot, chatID, "Xatolik. Qayta urinib ko'ring.")
		return
	}

	// Rate limit: only 1 active (PENDING) ride request per rider
	var existing int
	if err := db.QueryRowContext(context.Background(), `SELECT 1 FROM ride_requests WHERE rider_user_id = ?1 AND status = ?2 LIMIT 1`, userID, domain.RequestStatusPending).Scan(&existing); err == nil {
		send(bot, chatID, "Sizda allaqachon faol so'rov bor. Haydovchi topilguncha yoki bekor qilinguncha kuting.")
		return
	}

	requestID := uuid.New()
	expiresAt := time.Now().Add(time.Duration(cfg.RequestExpiresSeconds) * time.Second)
	pickupGrid := utils.GridID(lat, lng)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO ride_requests (id, rider_user_id, pickup_lat, pickup_lng, radius_km, status, expires_at, pickup_grid)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		requestID.String(), userID, lat, lng, cfg.MatchRadiusKm, domain.RequestStatusPending, expiresAt, pickupGrid)
	if err != nil {
		log.Printf("rider: create request: %v", err)
		send(bot, chatID, "Xatolik. So‘rov yuborilmadi.")
		return
	}

	if matchService != nil {
		if err := matchService.BroadcastRequest(context.Background(), requestID.String()); err != nil {
			log.Printf("rider: broadcast request: %v", err)
		}
	}

	send(bot, chatID, "So‘rov ketdi. Hozir yaqin haydovchilarga yubordim.")

	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnCancel),
		),
	)
	keyboard.ResizeKeyboard = true
	m := tgbotapi.NewMessage(chatID, "Haydovchi topilguncha kuting. Bekor qilish tugmasini bosishingiz mumkin.")
	m.ReplyMarkup = keyboard
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send: %v", err)
	}
}

func handleCancel(bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, tripService *services.TripService, chatID, telegramID int64) {
	_ = cfg
	ctx := context.Background()
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?1`, telegramID).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return
		}
		log.Printf("rider: get user: %v", err)
		send(bot, chatID, "Xatolik.")
		return
	}
	// If rider has an active trip (WAITING or STARTED), cancel the trip first ("safarni bekor qilish").
	if tripService != nil {
		var tripID string
		err := db.QueryRowContext(ctx, `
			SELECT id FROM trips
			WHERE rider_user_id = ?1 AND status IN (?2, ?3)
			ORDER BY id DESC LIMIT 1`,
			userID, domain.TripStatusWaiting, domain.TripStatusStarted).Scan(&tripID)
		if err == nil && tripID != "" {
			result, err := tripService.CancelByRider(ctx, tripID, userID)
			if err != nil {
				log.Printf("rider: cancel trip: %v", err)
				send(bot, chatID, "Xatolik.")
				return
			}
			if result != nil {
				send(bot, chatID, "Safar bekor qilindi.")
				if ensureRiderPhone(bot, db, chatID, telegramID) {
					return
				}
				sendMainMenu(bot, chatID)
				return
			}
		}
	}
	res, err := db.ExecContext(ctx, `
		UPDATE ride_requests SET status = ?1
		WHERE id = (
			SELECT id FROM ride_requests
			WHERE rider_user_id = ?2 AND status = ?3
			ORDER BY created_at DESC LIMIT 1
		)`,
		domain.RequestStatusCancelled, userID, domain.RequestStatusPending)
	if err != nil {
		log.Printf("rider: cancel request: %v", err)
		send(bot, chatID, "Xatolik.")
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		send(bot, chatID, "Bekor qilinadigan so‘rov topilmadi.")
		return
	}
	send(bot, chatID, "Bekor qilindi.")
	if ensureRiderPhone(bot, db, chatID, telegramID) {
		return
	}
	sendMainMenu(bot, chatID)
}

func pollAndNotifyRider(ctx context.Context, bot *tgbotapi.BotAPI, db *sql.DB, cfg *config.Config, notified *notifiedState) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
	notifyTripUpdates(bot, db, notified)
		}
	}
}

// notifyTripUpdates is unused: trip lifecycle (start/finish) is notified by services.TripService.
func notifyTripUpdates(bot *tgbotapi.BotAPI, db *sql.DB, notified *notifiedState) {}

func formatSummary(km float64, fareAmount int64) string {
	return fmt.Sprintf("Safar tugadi.\n%s\nNarx: %d", formatKm(km), fareAmount)
}

func formatKm(km float64) string {
	return fmt.Sprintf("%.2f km", km)
}

func send(bot *tgbotapi.BotAPI, chatID int64, text string) {
	m := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(m); err != nil {
		log.Printf("rider: send to %d: %v", chatID, err)
	}
}
