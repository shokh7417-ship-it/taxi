package handlers

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/utils"
)

// DriverReferralLink returns a handler that responds with the authenticated driver's referral link.
// If the user has no referral_code, one is generated and saved (backfill for users created before referral fields).
func DriverReferralLink(db *sql.DB, driverBot *tgbotapi.BotAPI) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.UserID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		ctx := c.Request.Context()

		var referralCode sql.NullString
		err := db.QueryRowContext(ctx, `SELECT referral_code FROM users WHERE id = ?1`, u.UserID).Scan(&referralCode)
		if err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
				return
			}
			log.Printf("referral: get code: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get referral code"})
			return
		}

		code := referralCode.String
		if !referralCode.Valid || code == "" {
			code, err = utils.GenerateReferralCode(ctx, db)
			if err != nil {
				log.Printf("referral: generate code: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate referral code"})
				return
			}
			_, err = db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, u.UserID)
			if err != nil {
				log.Printf("referral: update code: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save referral code"})
				return
			}
		}

		botUsername := ""
		if driverBot != nil {
			botUsername = driverBot.Self.UserName
		}
		link := utils.ReferralLink(botUsername, code)
		if link == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "referral link not available"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"referral_link": link})
	}
}

// RiderReferralLink returns a handler that responds with the authenticated rider's referral link.
// If the user has no referral_code, one is generated and saved (backfill).
func RiderReferralLink(db *sql.DB, riderBot *tgbotapi.BotAPI) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := auth.UserFromContext(c.Request.Context())
		if u == nil || u.UserID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		ctx := c.Request.Context()

		var referralCode sql.NullString
		err := db.QueryRowContext(ctx, `SELECT referral_code FROM users WHERE id = ?1`, u.UserID).Scan(&referralCode)
		if err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
				return
			}
			log.Printf("referral: get code: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get referral code"})
			return
		}

		code := referralCode.String
		if !referralCode.Valid || code == "" {
			code, err = utils.GenerateReferralCode(ctx, db)
			if err != nil {
				log.Printf("referral: generate code: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate referral code"})
				return
			}
			_, err = db.ExecContext(ctx, `UPDATE users SET referral_code = ?1 WHERE id = ?2`, code, u.UserID)
			if err != nil {
				log.Printf("referral: update code: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save referral code"})
				return
			}
		}

		botUsername := ""
		if riderBot != nil {
			botUsername = riderBot.Self.UserName
		}
		link := utils.ReferralLink(botUsername, code)
		if link == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "referral link not available"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"referral_link": link})
	}
}
