package ws

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"taxi-mvp/internal/auth"
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
func ServeWsWithAuth(hub *Hub, db *sql.DB, driverBotToken, riderBotToken string, w http.ResponseWriter, r *http.Request) {
	tripID := strings.TrimSpace(r.URL.Query().Get("trip_id"))
	if tripID == "" {
		http.Error(w, "trip_id required", http.StatusBadRequest)
		return
	}
	initData := r.Header.Get(headerInitData)
	if initData == "" {
		initData = r.URL.Query().Get("init_data")
	}
	if initData == "" {
		logger.AuthFailure("missing init data")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"missing init data"}`))
		return
	}
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
	ctx := r.Context()
	userID, role, err := auth.ResolveUserFromTelegramID(ctx, db, telegramUserID)
	if err != nil {
		logger.AuthFailure("user not found")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"user not found"}`))
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
