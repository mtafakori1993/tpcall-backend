package signaling

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 25 * time.Second
	maxMessageSize = 256 << 10
	sendQueueSize  = 64
)

// Client wraps a single authenticated WebSocket connection.
// All outbound writes go through the bounded send channel consumed by the
// single writePump goroutine, so no other goroutine ever touches the socket
// directly. Sender identity is fixed at connect time from the JWT and can
// never be changed by the client.
type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	userID    uuid.UUID
	extension string
	name      string
	connID    string

	done      chan struct{}
	closeOnce sync.Once
}

func newClient(hub *Hub, conn *websocket.Conn, userID uuid.UUID, extension, name string) *Client {
	return &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan []byte, sendQueueSize),
		userID:    userID,
		extension: extension,
		name:      name,
		connID:    uuid.NewString(),
		done:      make(chan struct{}),
	}
}

// close requests a graceful shutdown: the writePump drains the send queue,
// writes a close frame, and closes the socket. It is safe to call from
// multiple goroutines.
func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

// writePump is the only goroutine that writes to the socket.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			// Drain any queued messages (e.g. session_replaced) then close.
			for {
				select {
				case msg := <-c.send:
					_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
					if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						return
					}
				default:
					_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
					_ = c.conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
					return
				}
			}
		}
	}
}

// readPump is the only goroutine that reads from the socket.
func (c *Client) readPump() {
	defer func() {
		c.close()
		c.hub.handleDisconnect(c)
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		c.hub.handleMessage(c, data)
	}
}
