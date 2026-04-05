package ws

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"taxi-mvp/internal/auth"
	"taxi-mvp/internal/domain"
	"taxi-mvp/internal/logger"
)

const headerInitData = "X-Telegram-Init-Data"

// ServeWs handles GET /ws?trip_id=xxx and upgrades to WebSocket (no auth). Use ServeWsWithAuth for protected WS.
func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	tripID := strings.TrimSpace(r.URL.Query().Get("trip_id"))
	if tripID == "" {
		http.Error(w, "trip_id required", http.StatusBadRequest)
		return
	}
	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &client{
		hub:    hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		tripIDs: map[string]struct{}{tripID: {}},
	}
	client.hub.register <- client
	go client.writePump()
	client.readPump()
}

// ServeWsWithAuth requires Telegram Mini App auth; only rider or assigned driver of the trip may subscribe.
// When enableDriverIDHeader is true and init data is absent, accepts header X-Driver-Id (internal driver user id) for drivers only.
func ServeWsWithAuth(hub *Hub, db *sql.DB, driverBotToken, riderBotToken string, enableDriverIDHeader bool, w http.ResponseWriter, r *http.Request) {
	tripID := strings.TrimSpace(r.URL.Query().Get("trip_id"))
	if tripID == "" {
		http.Error(w, "trip_id required", http.StatusBadRequest)
		return
	}
	initData := r.Header.Get(headerInitData)
	if initData == "" {
		initData = r.URL.Query().Get("init_data")
	}
	ctx := r.Context()
	var userID int64
	var role string

	if initData != "" {
		var telegramUserID int64
		var err error
		telegramUserID, err = auth.VerifyMiniAppInitData(driverBotToken, initData)
		if err != nil {
			telegramUserID, err = auth.VerifyMiniAppInitData(riderBotToken, initData)
		}
		if err != nil {
			logger.AuthFailure("invalid init data")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid init data"}`))
			return
		}
		userID, role, err = auth.ResolveUserFromTelegramID(ctx, db, telegramUserID)
		if err != nil {
			logger.AuthFailure("user not found")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"user not found"}`))
			return
		}
	} else if enableDriverIDHeader {
		driverIDStr := strings.TrimSpace(r.Header.Get(auth.HeaderDriverID))
		if driverIDStr == "" {
			logger.AuthFailure("missing init data")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"missing init data"}`))
			return
		}
		parsed, err := strconv.ParseInt(driverIDStr, 10, 64)
		if err != nil || parsed <= 0 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid driver id"}`))
			return
		}
		if _, err := auth.ResolveDriverByUserID(ctx, db, parsed); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"user not found"}`))
			return
		}
		userID = parsed
		role = domain.RoleDriver
	} else {
		logger.AuthFailure("missing init data")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"missing init data"}`))
		return
	}

	allowed, err := auth.AuthorizeTripAccess(ctx, db, userID, tripID, role)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !allowed {
		logger.AuthFailure("not authorized for this trip", slog.String("trip_id", tripID), slog.Int64("user_id", userID))
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"not authorized for this trip"}`))
		return
	}
	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &client{
		hub:     hub,
		conn:    conn,
		send:    make(chan []byte, 256),
		tripIDs: map[string]struct{}{tripID: {}},
		userID:  userID,
	}
	client.hub.register <- client
	logger.WebSocketEvent("connect", tripID, userID)
	go client.writePump()
	client.readPump()
}

func (c *client) readPump() {
	defer func() {
		for tripID := range c.tripIDs {
			logger.WebSocketEvent("disconnect", tripID, c.userID)
			break
		}
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(msg)
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
