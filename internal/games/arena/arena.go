// Package arena is a real-time multiplayer game built on the Phoenix SDK: players
// are dots on a field, move with the arrow keys, and grow by eating food. It
// complements chess by exercising the realtime path — frequent position events,
// presence, and high event throughput — all on the same backend. Everything is
// deterministic (no rand) so the match stays fully replayable from the log.
package arena

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/fazal/phoenix/pkg/phoenix"
)

const (
	fieldW    = 1000
	fieldH    = 640
	foodCount = 16
	playerR   = 16.0
	foodR     = 9.0
	maxStep   = 70.0 // max distance one move intent may travel (anti-teleport)
)

var palette = []string{"#ff6b3d", "#3ddc84", "#4d9bff", "#ffd23d", "#c061ff", "#ff5d8f", "#2dd4bf", "#f97316"}

type player struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Score int     `json:"score"`
	Name  string  `json:"name"`
	Color string  `json:"color"`
}

type food struct {
	ID int     `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

type state struct {
	W       int                `json:"w"`
	H       int                `json:"h"`
	Players map[string]*player `json:"players"`
	Food    []food             `json:"food"`
	NextID  int                `json:"-"` // deterministic food-id counter (not sent)
}

// Register adds the "arena" game to the engine.
func Register(app *phoenix.Engine) {
	app.Game("arena").
		InitialState(func() any { return newState() }).
		SnapshotEvery(50).
		RestoreState(func(b []byte) any {
			var s state
			if err := json.Unmarshal(b, &s); err != nil {
				return nil
			}
			if s.Players == nil {
				s.Players = map[string]*player{}
			}
			s.NextID = len(s.Food)
			return s
		}).
		OnEvent("PlayerJoined", join).
		OnEvent("PlayerLeft", leave).
		OnEvent("move", move)
}

func newState() state {
	s := state{W: fieldW, H: fieldH, Players: map[string]*player{}, NextID: 0}
	for i := 0; i < foodCount; i++ {
		x, y := foodPos(i)
		s.Food = append(s.Food, food{ID: i, X: x, Y: y})
		s.NextID++
	}
	return s
}

// foodPos derives a deterministic position for food index n (no rand, so replay
// reproduces the exact field).
func foodPos(n int) (float64, float64) {
	a := (n*1103515245 + 12345) & 0x7fffffff
	b := ((n+7)*1103515245 + 54321) & 0x7fffffff
	x := 24 + float64(a%(fieldW-48))
	y := 24 + float64(b%(fieldH-48))
	return x, y
}

func join(s any, e phoenix.Event) (any, error) {
	st := s.(state)
	if st.Players == nil {
		st.Players = map[string]*player{}
	}
	if _, ok := st.Players[e.PlayerID]; !ok {
		var p struct {
			DisplayName string `json:"display_name"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		idx := len(st.Players)
		px, py := foodPos(900 + idx) // deterministic spawn
		st.Players[e.PlayerID] = &player{X: px, Y: py, Name: p.DisplayName, Color: palette[idx%len(palette)]}
	}
	return st, nil
}

func leave(s any, e phoenix.Event) (any, error) {
	st := s.(state)
	delete(st.Players, e.PlayerID)
	return st, nil
}

func move(s any, e phoenix.Event) (any, error) {
	st := s.(state)
	p := st.Players[e.PlayerID]
	if p == nil {
		return st, errors.New("not in the arena")
	}

	var in struct{ X, Y float64 }
	if err := json.Unmarshal(e.Payload, &in); err != nil {
		return st, errors.New("bad move payload")
	}

	// Clamp the target into the field, then cap the step length (anti-teleport).
	in.X = clamp(in.X, playerR, fieldW-playerR)
	in.Y = clamp(in.Y, playerR, fieldH-playerR)
	dx, dy := in.X-p.X, in.Y-p.Y
	if d := math.Hypot(dx, dy); d > maxStep && d > 0 {
		in.X = p.X + dx/d*maxStep
		in.Y = p.Y + dy/d*maxStep
	}
	p.X, p.Y = in.X, in.Y

	// Eat any food within reach; respawn it at a fresh deterministic spot.
	for i := range st.Food {
		if math.Hypot(p.X-st.Food[i].X, p.Y-st.Food[i].Y) < playerR+foodR {
			p.Score++
			nx, ny := foodPos(st.NextID)
			st.Food[i] = food{ID: st.NextID, X: nx, Y: ny}
			st.NextID++
			break
		}
	}
	return st, nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
