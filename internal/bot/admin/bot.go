package admin

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/config"
	"taxi-mvp/internal/services"
)

const (
	btnFareMenu       = "💰 Narx belgilash"
	btnBaseFare       = "🚕 Start narxi"
	btnTier0_1        = "1️⃣ 0–1 km narxi"
	btnTier1_2        = "2️⃣ 1–2 km narxi"
	btnTier2Plus      = "♾ 2 km dan yuqori narx"
	btnCommissionPct  = "📊 Komissiya %"
	btnViewTariff     = "📄 Joriy tarifni ko'rish"
	btnBack           = "◀️ Orqaga"
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

// Run starts the admin bot. Only cfg.AdminID can use fare menu. If AdminBotToken is empty, do not call Run.
func Run(ctx context.Context, cfg *config.Config, bot *tgbotapi.BotAPI, fareSvc *services.FareService) error {
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
			handleUpdate(bot, cfg, fareSvc, state, update)
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, cfg *config.Config, fareSvc *services.FareService, state *fareEditState, update tgbotapi.Update) {
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
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "⛔ Sizga ruxsat yo'q."))
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
		sendMessage(bot, chatID, "Yangi start narxini kiriting (so'm):")
	case btnTier0_1:
		state.set(fromID, "tier_0_1")
		sendMessage(bot, chatID, "0–1 km uchun narxni kiriting (so'm/km):")
	case btnTier1_2:
		state.set(fromID, "tier_1_2")
		sendMessage(bot, chatID, "1–2 km uchun narxni kiriting (so'm/km):")
	case btnTier2Plus:
		state.set(fromID, "tier_2_plus")
		sendMessage(bot, chatID, "2 km dan yuqori uchun narxni kiriting (so'm/km):")
	case btnCommissionPct:
		state.set(fromID, "commission_percent")
		sendMessage(bot, chatID, "Komissiya foizini kiriting (0–100):")
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

func handleNumericInput(bot *tgbotapi.BotAPI, cfg *config.Config, fareSvc *services.FareService, state *fareEditState, chatID, adminTelegramID int64, text, field string) {
	val, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		sendMessage(bot, chatID, "Iltimos, butun son kiriting.")
		return
	}
	if field != "commission_percent" && val < 0 {
		sendMessage(bot, chatID, "Iltimos, musbat butun son kiriting (so'm).")
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
			sendMessage(bot, chatID, "Iltimos, 0 dan 100 gacha butun son kiriting.")
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
		sendMessage(bot, chatID, "Xatolik: yangilash amalga oshmadi.")
		return
	}
	sendMessage(bot, chatID, "✅ Yangilandi.")
	sendCurrentTariff(bot, fareSvc, chatID)
	sendFareSubmenu(bot, chatID)
}

func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnFareMenu)),
	)
	kb.ResizeKeyboard = true
	msg := tgbotapi.NewMessage(chatID, "Admin panel. Quyidagi tugmalardan foydalaning:")
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
	msg := tgbotapi.NewMessage(chatID, "Narx sozlamalari:")
	msg.ReplyMarkup = kb
	if _, err := bot.Send(msg); err != nil {
		log.Printf("admin bot: send fare submenu: %v", err)
	}
}

func sendCurrentTariff(bot *tgbotapi.BotAPI, fareSvc *services.FareService, chatID int64) {
	ctx := context.Background()
	settings, err := fareSvc.GetFareSettings(ctx)
	if err != nil {
		sendMessage(bot, chatID, "Tarifni o'qishda xatolik.")
		return
	}
	text := fmt.Sprintf(
		"📄 Joriy tarif:\n\n🚕 Start narxi: %d so'm\n1️⃣ 0–1 km: %d so'm/km\n2️⃣ 1–2 km: %d so'm/km\n♾ 2+ km: %d so'm/km\n\n📊 Komissiya: %d%%",
		settings.BaseFare, settings.Tier0_1Km, settings.Tier1_2Km, settings.Tier2PlusKm, settings.CommissionPercent,
	)
	sendMessage(bot, chatID, text)
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	if _, err := bot.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("admin bot: send: %v", err)
	}
}
