package gateway

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// wsConn adapts a coder/websocket connection to the state.Conn interface. All
// writes go through a single writer goroutine (writePump) because the websocket
// library forbids concurrent writes; Send only enqueues.
type wsConn struct {
	connID   string
	playerID string
	display  string
	roomID   string
	ws       *websocket.Conn
	out      chan []byte
	cancel   context.CancelFunc
}

func (c *wsConn) ConnID() string      { return c.connID }
func (c *wsConn) PlayerID() string    { return c.playerID }
func (c *wsConn) DisplayName() string { return c.display }
func (c *wsConn) RoomID() string      { return c.roomID }

// Send enqueues a message for the writer goroutine. Non-blocking: if the client
// is too slow and the buffer is full, the message is dropped rather than
// stalling the hub (the client can resync from a snapshot on reconnect).
func (c *wsConn) Send(msg []byte) {
	select {
	case c.out <- msg:
	default:
	}
}

// writePump serializes all socket writes and drives the heartbeat. A failed
// write or ping cancels the connection, which unblocks the read pump.
func (c *wsConn) writePump(ctx context.Context, heartbeat time.Duration) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	defer c.ws.Close(websocket.StatusNormalClosure, "")

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.out:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, heartbeat)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}
