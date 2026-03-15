package auth

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/domain"
)

// HeaderInitData is the typical header name for Telegram Mini App init data.
const HeaderInitData = "X-Telegram-Init-Data"

// HeaderDriverID is an optional header for Mini App requests: internal driver user_id (users.id).
// Only used when ENABLE_DRIVER_ID_HEADER is true and init data is missing. Use only if you trust the Mini App URL.
const HeaderDriverID = "X-Driver-Id"

// RequireMiniAppAuth returns Gin middleware that verifies Telegram initData, resolves the user, and sets context.
// botToken is the token of the bot that opened the Mini App (driver or rider bot).
// If verification or user resolution fails, responds with 401 and aborts.
func RequireMiniAppAuth(db *sql.DB, botToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		initData := c.GetHeader(HeaderInitData)
		if initData == "" {
			initData = c.Query("init_data")
		}
		if initData == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing init data"})
			return
		}
		telegramUserID, err := VerifyMiniAppInitData(botToken, initData)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid init data"})
			return
		}
		userID, role, err := ResolveUserFromTelegramID(c.Request.Context(), db, telegramUserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}
		c.Request = c.Request.WithContext(WithUser(c.Request.Context(), &User{
			UserID:         userID,
			TelegramUserID: telegramUserID,
			Role:           role,
		}))
		c.Next()
	}
}

// RequireDriverAuth sets the driver in the request context using either:
// 1) Already-set user (e.g. by TryDriverIDHeader from X-Driver-Id): if context has a user, continue.
// 2) Telegram initData: reads X-Telegram-Init-Data (or init_data query), validates with driver bot token,
//    maps Telegram user id to internal user_id and role, then sets that user in context.
// 3) X-Driver-Id (optional): if enableDriverIDHeader is true and init data is missing, reads X-Driver-Id
//    (internal users.id of the driver), verifies the user is a driver, and sets them in context.
// Use TryDriverIDHeader before this middleware so Mini App requests with X-Driver-Id work without initData.
func RequireDriverAuth(db *sql.DB, driverBotToken string, enableDriverIDHeader bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if u := UserFromContext(c.Request.Context()); u != nil && u.Role == domain.RoleDriver {
			c.Next()
			return
		}
		initData := c.GetHeader(HeaderInitData)
		if initData == "" {
			initData = c.Query("init_data")
		}
		if initData != "" {
			telegramUserID, err := VerifyMiniAppInitData(driverBotToken, initData)
			if err == nil {
				userID, role, err := ResolveUserFromTelegramID(c.Request.Context(), db, telegramUserID)
				if err == nil {
					c.Request = c.Request.WithContext(WithUser(c.Request.Context(), &User{
						UserID:         userID,
						TelegramUserID: telegramUserID,
						Role:           role,
					}))
					c.Next()
					return
				}
			}
		}
		if enableDriverIDHeader {
			driverIDRaw := strings.TrimSpace(c.GetHeader(HeaderDriverID))
			if driverIDRaw != "" {
				userID, err := strconv.ParseInt(driverIDRaw, 10, 64)
				if err == nil && userID > 0 {
					_, err := ResolveDriverByUserID(c.Request.Context(), db, userID)
					if err == nil {
						c.Request = c.Request.WithContext(WithUser(c.Request.Context(), &User{
							UserID:         userID,
							TelegramUserID: 0,
							Role:           domain.RoleDriver,
						}))
						c.Next()
						return
					}
				}
			}
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid init data; or driver id not allowed or invalid"})
	}
}

// RequireRiderAuth uses the rider bot token for verification (e.g. rider Mini App or rider client).
func RequireRiderAuth(db *sql.DB, riderBotToken string) gin.HandlerFunc {
	return RequireMiniAppAuth(db, riderBotToken)
}
