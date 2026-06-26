// Package bots runs server-side demo players so the dashboard is always lively
// for a demo. A bot is just an in-process connection attached to the hub that
// sends intents on a timer — it drives the real event->reducer->broadcast path,
// so everything (live spectate, presence, event throughput, snapshots) lights up
// exactly as it would for human players.
package bots

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/fazal/phoenix/internal/auth"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/room"
	"github.com/fazal/phoenix/internal/state"
	"github.com/google/uuid"
)

type Runner struct {
	hub   *state.Hub
	rooms *room.Service
	auth  *auth.Service
}

func NewRunner(hub *state.Hub, rooms *room.Service, a *auth.Service) *Runner {
	return &Runner{hub: hub, rooms: rooms, auth: a}
}

// botConn is an in-process state.Conn for a bot. It discards server messages
// (arena bots don't need to read state — they just wander).
type botConn struct {
	connID, playerID, name, roomID string
}

func (c *botConn) ConnID() string      { return c.connID }
func (c *botConn) PlayerID() string    { return c.playerID }
func (c *botConn) DisplayName() string { return c.name }
func (c *botConn) RoomID() string      { return c.roomID }
func (c *botConn) Send([]byte)         {}

// LaunchArena creates an arena room and fills it with n wandering bots for the
// given duration. Returns the room id so the caller can point the dashboard at it.
func (r *Runner) LaunchArena(n int, dur time.Duration) (string, error) {
	if n < 1 {
		n = 6
	}
	if n > 20 {
		n = 20
	}
	ctx := context.Background()
	rm, err := r.rooms.Create(ctx, room.CreateParams{GameType: "arena", MaxPlayers: 50})
	if err != nil {
		return "", err
	}
	for i := 0; i < n; i++ {
		u, _, err := r.auth.GuestLogin(ctx, fmt.Sprintf("Bot-%d", i+1))
		if err != nil {
			continue
		}
		c := &botConn{connID: uuid.NewString(), playerID: u.ID, name: u.DisplayName, roomID: rm.ID}
		_ = r.hub.Join(ctx, c, "arena", false)
		go r.driveArena(ctx, c, dur)
	}
	return rm.ID, nil
}

func (r *Runner) driveArena(ctx context.Context, c *botConn, dur time.Duration) {
	x, y := 500.0+rand.Float64()*100-50, 320.0+rand.Float64()*100-50
	tx, ty := randTarget()
	ticker := time.NewTicker(110 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(dur)

	for time.Now().Before(deadline) {
		<-ticker.C
		dx, dy := tx-x, ty-y
		d := math.Hypot(dx, dy)
		if d < 12 {
			tx, ty = randTarget() // reached target — pick a new one
		} else {
			step := 42.0
			x += dx / d * step
			y += dy / d * step
		}
		msg := fmt.Sprintf(`{"type":"move","payload":{"x":%.0f,"y":%.0f}}`, x, y)
		r.hub.HandleMessage(ctx, c, []byte(msg))
	}
	r.hub.Detach(c)
	r.hub.Leave(ctx, core.Player{ID: c.playerID, DisplayName: c.name, RoomID: c.roomID})
}

func randTarget() (float64, float64) {
	return 40 + rand.Float64()*920, 40 + rand.Float64()*560
}
