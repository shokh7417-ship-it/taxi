package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/accounting"
	"taxi-mvp/internal/config"
	driverbot "taxi-mvp/internal/bot/driver"
	"taxi-mvp/internal/repositories"
	"taxi-mvp/internal/services"
)

const (
	btnFareMenu       = "💰 Нарх белгилаш"
	btnBaseFare       = "🚕 Старт нархи"
	btnTier0_1        = "1️⃣ 0–1 км нархи"
	btnTier1_2        = "2️⃣ 1–2 км нархи"
	btnTier2Plus      = "♾ 2 км дан юқори нарх"
	btnCommissionPct  = "📊 Комиссия %"
	btnViewTariff     = "📄 Жорий тарифни кўриш"
	btnBack           = "◀️ Орқага"
)

// pendingEdit indicates which fare field the admin is editing (value is the field key).
type fareEditState struct {
	mu    sync.Mutex
	field map[int64]string // telegram user id -> "base_fare" | "tier_0_1" | "tier_1_2" | "tier_2_plus" | "commission_percent"
}

func (s *fareEditState) set(telegramID int64, field string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.field == nil {
		s.field = make(map[int64]string)
	}
	s.field[telegramID] = field
}

func (s *fareEditState) get(telegramID int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.field[telegramID]
	return f, ok
}

func (s *fareEditState) clear(telegramID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.field, telegramID)
}

// Run starts the admin bot. driverBot is used to send messages to drivers (approval/reject); admin bot must not message drivers (chat not found).
func Run(ctx context.Context, cfg *config.Config, db *sql.DB, bot *tgbotapi.BotAPI, fareSvc *services.FareService, driverBot *tgbotapi.BotAPI) error {
	if cfg == nil || cfg.AdminID == 0 || fareSvc == nil {
		return nil
	}
	log.Printf("admin bot: started @%s (admin_id=%d)", bot.Self.UserName, cfg.AdminID)
	state := &fareEditState{}
	updates := bot.GetUpdatesChan(tgbotapi.NewUpdate(0))
	for {
		select {
		case <-ctx.Done():
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			handleUpdate(bot, cfg, db, fareSvc, driverBot, state, update)
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, cfg *config.Config, db *sql.DB, fareSvc *services.FareService, driverBot *tgbotapi.BotAPI, state *fareEditState, update tgbotapi.Update) {
	// Handle callback queries (approve/reject driver verification) first.
	if update.CallbackQuery != nil {
		handleApprovalCallback(bot, cfg, db, driverBot, update.CallbackQuery)
		return
	}
	var chatID int64
	var fromID int64
	if update.Message != nil {
		chatID = update.Message.Chat.ID
		if update.Message.From != nil {
			fromID = update.Message.From.ID
		}
	}
	if fromID == 0 {
		return
	}
	if fromID != cfg.AdminID {
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "⛔ Сизга рухсат йўқ."))
		return
	}

	// Check if we are waiting for a numeric value for a field
	if update.Message != nil && update.Message.Text != "" {
		if field, ok := state.get(fromID); ok {
			handleNumericInput(bot, cfg, fareSvc, state, chatID, fromID, update.Message.Text, field)
			return
		}
	}

	if update.Message == nil || update.Message.Text == "" {
		return
	}
	text := strings.TrimSpace(update.Message.Text)

	switch text {
	case "/start":
		sendMainMenu(bot, chatID)
	case btnFareMenu:
		sendFareSubmenu(bot, chatID)
	case btnBaseFare:
		state.set(fromID, "base_fare")
		sendMessage(bot, chatID, "Янги старт нархини киритинг (сўм):")
	case btnTier0_1:
		state.set(fromID, "tier_0_1")
		sendMessage(bot, chatID, "0–1 км учун нархни киритинг (сўм/км):")
	case btnTier1_2:
		state.set(fromID, "tier_1_2")
		sendMessage(bot, chatID, "1–2 км учун нархни киритинг (сўм/км):")
	case btnTier2Plus:
		state.set(fromID, "tier_2_plus")
		sendMessage(bot, chatID, "2 км дан юқори учун нархни киритинг (сўм/км):")
	case btnCommissionPct:
		state.set(fromID, "commission_percent")
		sendMessage(bot, chatID, "Комиссия фоизини киритинг (0–100):")
	case btnViewTariff:
		sendCurrentTariff(bot, fareSvc, chatID)
	case btnBack:
		state.clear(fromID)
		sendMainMenu(bot, chatID)
	default:
		// If not in edit state, show main menu
		state.clear(fromID)
		sendMainMenu(bot, chatID)
	}
}

func handleApprovalCallback(bot *tgbotapi.BotAPI, cfg *config.Config, db *sql.DB, driverBot *tgbotapi.BotAPI, q *tgbotapi.CallbackQuery) {
	if bot == nil || cfg == nil || db == nil || q == nil {
		return
	}
	// Answer callback immediately to stop retries/spam.
	if q.ID != "" {
		_, _ = bot.Request(tgbotapi.NewCallback(q.ID, ""))
	}
	if q.From == nil || q.From.ID != cfg.AdminID {
		return
	}
	data := q.Data
	if !strings.HasPrefix(data, "approve_driver_") && !strings.HasPrefix(data, "reject_driver_") {
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
	var driverTgID int64
	var currentStatus string
	var approvalNotified int
	if err := db.QueryRowContext(ctx, `
		SELECT u.telegram_id, COALESCE(d.verification_status, ''), COALESCE(d.approval_notified, 0)
		FROM drivers d JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ?1`, driverUserID).Scan(&driverTgID, &currentStatus, &approvalNotified); err != nil {
		log.Printf("admin bot: load driver for verify callback user_id=%d: %v", driverUserID, err)
		return
	}
	if strings.HasPrefix(data, "approve_driver_") {
		if currentStatus == "approved" {
			// Already approved: just reflect this in admin message if possible.
			if q.Message != nil {
				edit := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID,
					fmt.Sprintf("✅ Ҳайдовчи аллақачон тасдиқланган (user_id=%d).", driverUserID))
				_, _ = bot.Request(edit)
			}
			return
		}
		// Approve driver.
		if _, err := db.ExecContext(ctx, `UPDATE drivers SET verification_status = 'approved' WHERE user_id = ?1`, driverUserID); err != nil {
			log.Printf("admin bot: approve driver update error user_id=%d: %v", driverUserID, err)
			return
		}
		if err := accounting.TryGrantSignupPromoOnce(ctx, db, driverUserID); err != nil {
			log.Printf("admin bot: signup promo grant user_id=%d: %v", driverUserID, err)
		}
		// Do not send to driver from admin bot (driver has no chat with admin bot → "chat not found").
		// Driver approval notifier sends approval + bonus + keyboard via driver bot.

		// Update admin message to show success and remove buttons.
		if q.Message != nil {
			editText := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID,
				fmt.Sprintf("✅ Ҳайдовчи тасдиқланди (user_id=%d).", driverUserID))
			_, _ = bot.Request(editText)
			clearMarkup := tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID, tgbotapi.InlineKeyboardMarkup{})
			_, _ = bot.Request(clearMarkup)
		}
		return
	}

	// reject_driver_
	if currentStatus == "approved" {
		// Already approved: reflect in admin message if possible.
		if q.Message != nil {
			edit := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID,
				fmt.Sprintf("✅ Ҳайдовчи аллақачон тасдиқланган (user_id=%d).", driverUserID))
			_, _ = bot.Request(edit)
		}
		return
	}
	adminRepo := repositories.NewAdminDriverRepository(db)
	if err := adminRepo.DeleteAndResetDriverApplication(ctx, driverUserID); err != nil {
		if errors.Is(err, repositories.ErrDriverRejectNotAllowed) {
			log.Printf("admin bot: reject driver not allowed user_id=%d", driverUserID)
		} else {
			log.Printf("admin bot: reject driver delete/reset error user_id=%d: %v", driverUserID, err)
		}
		return
	}
	if driverTgID != 0 && driverBot != nil {
		driverbot.SendApplicationRejectedMessage(driverBot, driverTgID)
	}

	// Update admin message to show rejection and remove buttons.
	if q.Message != nil {
		editText := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID,
			fmt.Sprintf("❌ Ҳайдовчи рад этилди (user_id=%d).", driverUserID))
		_, _ = bot.Request(editText)
		clearMarkup := tgbotapi.NewEditMessageReplyMarkup(q.Message.Chat.ID, q.Message.MessageID, tgbotapi.InlineKeyboardMarkup{})
		_, _ = bot.Request(clearMarkup)
	}
}

func handleNumericInput(bot *tgbotapi.BotAPI, cfg *config.Config, fareSvc *services.FareService, state *fareEditState, chatID, adminTelegramID int64, text, field string) {
	val, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		sendMessage(bot, chatID, "Илтимос, бутун сон киритинг.")
		return
	}
	if field != "commission_percent" && val < 0 {
		sendMessage(bot, chatID, "Илтимос, мусбат бутун сон киритинг (сўм).")
		return
	}
	ctx := context.Background()
	switch field {
	case "base_fare":
		_, err = fareSvc.UpdateBaseFare(ctx, val, adminTelegramID)
	case "tier_0_1":
		_, err = fareSvc.UpdateTier0To1(ctx, val, adminTelegramID)
	case "tier_1_2":
		_, err = fareSvc.UpdateTier1To2(ctx, val, adminTelegramID)
	case "tier_2_plus":
		_, err = fareSvc.UpdateTier2Plus(ctx, val, adminTelegramID)
	case "commission_percent":
		if val < 0 || val > 100 {
			sendMessage(bot, chatID, "Илтимос, 0 дан 100 гача бутун сон киритинг.")
			state.clear(adminTelegramID)
			return
		}
		_, err = fareSvc.UpdateCommissionPercent(ctx, int(val), adminTelegramID)
	default:
		state.clear(adminTelegramID)
		sendMainMenu(bot, chatID)
		return
	}
	state.clear(adminTelegramID)
	if err != nil {
		log.Printf("admin bot: update fare %s: %v", field, err)
		sendMessage(bot, chatID, "Хатолик: янгилаш амалга ошмади.")
		return
	}
	sendMessage(bot, chatID, "✅ Янгиланди.")
	sendCurrentTariff(bot, fareSvc, chatID)
	sendFareSubmenu(bot, chatID)
}

func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnFareMenu)),
	)
	kb.ResizeKeyboard = true
	msg := tgbotapi.NewMessage(chatID, "Админ панели. Қуйидаги тугмалардан фойдаланинг:")
	msg.ReplyMarkup = kb
	if _, err := bot.Send(msg); err != nil {
		log.Printf("admin bot: send main menu: %v", err)
	}
}

func sendFareSubmenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnBaseFare),
			tgbotapi.NewKeyboardButton(btnTier0_1),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnTier1_2),
			tgbotapi.NewKeyboardButton(btnTier2Plus),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnCommissionPct),
			tgbotapi.NewKeyboardButton(btnViewTariff),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnBack),
		),
	)
	kb.ResizeKeyboard = true
	msg := tgbotapi.NewMessage(chatID, "Нарх созламалари:")
	msg.ReplyMarkup = kb
	if _, err := bot.Send(msg); err != nil {
		log.Printf("admin bot: send fare submenu: %v", err)
	}
}

func sendCurrentTariff(bot *tgbotapi.BotAPI, fareSvc *services.FareService, chatID int64) {
	ctx := context.Background()
	settings, err := fareSvc.GetFareSettings(ctx)
	if err != nil {
		sendMessage(bot, chatID, "Тарифни ўқишда хатолик.")
		return
	}
	text := fmt.Sprintf(
		"📄 Жорий тариф:\n\n🚕 Старт нархи: %d сўм\n1️⃣ 0–1 км: %d сўм/км\n2️⃣ 1–2 км: %d сўм/км\n♾ 2+ км: %d сўм/км\n\n📊 Комиссия: %d%%",
		settings.BaseFare, settings.Tier0_1Km, settings.Tier1_2Km, settings.Tier2PlusKm, settings.CommissionPercent,
	)
	sendMessage(bot, chatID, text)
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	if _, err := bot.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("admin bot: send: %v", err)
	}
}
