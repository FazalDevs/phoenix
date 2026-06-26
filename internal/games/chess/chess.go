// Package chessgame wires the chess rules onto the Phoenix SDK. It contains only
// game logic — Phoenix supplies auth, rooms, WebSockets, the event log, replay,
// matchmaking, and the admin API. Register it on an engine to host chess.
package chessgame

import (
	"encoding/json"
	"errors"

	"github.com/fazal/phoenix/internal/chess"
	"github.com/fazal/phoenix/pkg/phoenix"
)

// Register adds the "chess" game to the engine.
func Register(app *phoenix.Engine) {
	app.Game("chess").
		InitialState(func() any { return newChessState() }).
		SnapshotEvery(20).
		RestoreState(func(b []byte) any {
			var s chessState
			if err := json.Unmarshal(b, &s); err != nil {
				return nil
			}
			return s
		}).
		OnEvent("PlayerJoined", seatReducer).
		OnEvent("move", moveReducer).
		Derive(deriveChessEvents)
}

// chessState is the reduced, broadcast game state; its JSON shape is the contract
// the web client renders from. FEN is authoritative; the rest is derived.
type chessState struct {
	FEN      string   `json:"fen"`
	Turn     string   `json:"turn"`
	Status   string   `json:"status"`
	Players  seats    `json:"players"`
	Winner   string   `json:"winner"`
	LastMove *segment `json:"lastMove,omitempty"`
	History  []string `json:"history"`
}

type seats struct {
	W string `json:"w"`
	B string `json:"b"`
}

type segment struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func newChessState() chessState {
	return chessState{FEN: chess.NewGame().FEN(), Turn: "w", Status: "waiting", History: []string{}}
}

// seatReducer seats the first two joiners as white then black; the rest spectate.
func seatReducer(s any, e phoenix.Event) (any, error) {
	st := s.(chessState)
	if st.Players.W == "" {
		st.Players.W = e.PlayerID
	} else if st.Players.B == "" && e.PlayerID != st.Players.W {
		st.Players.B = e.PlayerID
	}
	if st.Players.W != "" && st.Players.B != "" && (st.Status == "waiting" || st.Status == "") {
		st.Status = "active"
	}
	return st, nil
}

func moveReducer(s any, e phoenix.Event) (any, error) {
	st := s.(chessState)
	if st.Status != "active" && st.Status != "check" {
		return st, errors.New("game is not active")
	}
	seat := ""
	switch e.PlayerID {
	case st.Players.W:
		seat = "w"
	case st.Players.B:
		seat = "b"
	}
	if seat == "" {
		return st, errors.New("spectators cannot move")
	}
	if seat != st.Turn {
		return st, errors.New("not your turn")
	}

	var mv struct{ From, To, Promotion string }
	if err := json.Unmarshal(e.Payload, &mv); err != nil {
		return st, errors.New("bad move payload")
	}
	g, err := chess.Load(st.FEN)
	if err != nil {
		return st, err
	}
	san, err := g.Move(mv.From, mv.To, mv.Promotion)
	if err != nil {
		return st, err // illegal move — rejected
	}

	st.FEN = g.FEN()
	st.Turn = g.Turn()
	st.LastMove = &segment{From: mv.From, To: mv.To}
	st.History = append(append([]string{}, st.History...), san)

	switch g.Status() {
	case "checkmate":
		st.Status = "checkmate"
		st.Winner = seat
	case "stalemate":
		st.Status = "stalemate"
	case "draw":
		st.Status = "draw"
	case "check":
		st.Status = "check"
	default:
		st.Status = "active"
	}
	return st, nil
}

func isTerminal(status string) bool {
	return status == "checkmate" || status == "stalemate" || status == "draw"
}

func deriveChessEvents(prev, next any, _ phoenix.Event) []phoenix.DerivedEvent {
	p, ok1 := prev.(chessState)
	n, ok2 := next.(chessState)
	if !ok1 || !ok2 {
		return nil
	}
	var out []phoenix.DerivedEvent
	if p.Status == "waiting" && n.Status == "active" {
		pl, _ := phoenix.NewPayload(map[string]any{"players": n.Players})
		out = append(out, phoenix.DerivedEvent{Type: "MatchStarted", Payload: pl})
	}
	if !isTerminal(p.Status) && isTerminal(n.Status) {
		pl, _ := phoenix.NewPayload(map[string]any{"winner": n.Winner, "players": n.Players, "reason": n.Status})
		out = append(out, phoenix.DerivedEvent{Type: "MatchEnded", Payload: pl})
	}
	return out
}
