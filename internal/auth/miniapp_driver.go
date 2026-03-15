package auth

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"taxi-mvp/internal/domain"
)

// TryDriverIDHeader is Gin middleware that sets the driver in request context from X-Driver-Id when present and valid.
// Use only if you trust requests carrying X-Driver-Id (e.g. from your Mini App over HTTPS).
// Verifies the user exists in the drivers table. Run this before RequireDriverAuth so that when
// the Mini App sends X-Driver-Id, Start/Cancel/Finish and driver location work without initData.
func TryDriverIDHeader(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
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
