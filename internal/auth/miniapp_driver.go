package auth

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/domain"
)

// TryDriverIDHeader is Gin middleware that sets the driver in request context from X-Driver-Id when present and valid.
// When enable is false (default production: ENABLE_DRIVER_ID_HEADER off), the header is ignored — same as Telegram-only deploys.
// When enable is true, verifies the user exists in the drivers table. Run this before RequireDriverAuth so that when
// the client sends X-Driver-Id, Start/Cancel/Finish and driver location work without initData.
func TryDriverIDHeader(db *sql.DB, enable bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enable {
			c.Next()
			return
		}
		driverIDStr := strings.TrimSpace(c.GetHeader(HeaderDriverID))
		if driverIDStr == "" {
			c.Next()
			return
		}
		userID, err := strconv.ParseInt(driverIDStr, 10, 64)
		if err != nil || userID <= 0 {
			c.Next()
			return
		}
		_, err = ResolveDriverByUserID(c.Request.Context(), db, userID)
		if err != nil {
			c.Next()
			return
		}
		c.Request = c.Request.WithContext(WithUser(c.Request.Context(), &User{
			UserID:         userID,
			TelegramUserID: 0,
			Role:           domain.RoleDriver,
		}))
		c.Next()
	}
}
