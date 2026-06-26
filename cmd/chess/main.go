// Command chess is a complete multiplayer chess backend built entirely on the
// Phoenix SDK. It contains only game rules — Phoenix supplies auth, rooms,
// WebSockets, the event log, replay, matchmaking, and the admin API. This is the
// "import the SDK, add rules, ship a backend" model: run this one binary and you
// have an authoritative, replayable, realtime chess server.
package main

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/fazal/phoenix/internal/chess"
	"github.com/fazal/phoenix/pkg/phoenix"
)

// chessState is the reduced, broadcast game state. Its JSON shape is the contract
// the web client renders from. The FEN is the authoritative position; everything
// else is derived for convenience.
type chessState struct {
	FEN      string   `json:"fen"`
	Turn     string   `json:"turn"`   // "w" | "b"
	Status   string   `json:"status"` // waiting | active | check | checkmate | stalemate | draw
	Players  seats    `json:"players"`
	Winner   string   `json:"winner"`             // "w" | "b" | ""
	LastMove *segment `json:"lastMove,omitempty"` // last from/to for board highlight
	History  []string `json:"history"`            // SAN move list
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
	return chessState{
		FEN:     chess.NewGame().FEN(),
		Turn:    "w",
		Status:  "waiting", // becomes active when both seats fill
		History: []string{},
	}
}

func main() {
	// Snapshot every 20 plies so reconnect/crash recovery folds only the tail of
	// the log instead of replaying the whole match.
	game := phoenix.New(phoenix.WithGameType("chess"), phoenix.WithSnapshotEvery(20))
	game.InitialState(func() any { return newChessState() })

	// Decode a snapshot's JSON back into the typed chess state.
	game.RestoreState(func(b []byte) any {
		var s chessState
		if err := json.Unmarshal(b, &s); err != nil {
			return nil
		}
		return s
	})

	game.OnJoin(func(p phoenix.Player) { log.Printf("join %s -> room %s", p.DisplayName, p.RoomID) })
	game.OnLeave(func(p phoenix.Player) { log.Printf("leave %s -> room %s", p.DisplayName, p.RoomID) })

	// Seat the first two joiners as white then black; the rest are spectators.
	// Folded from the PlayerJoined events, so seats are reconstructed identically
	// on replay/reconnect.
	game.OnEvent("PlayerJoined", func(s any, e phoenix.Event) (any, error) {
		st := s.(chessState)
		if st.Players.W == "" {
			st.Players.W = e.PlayerID
		} else if st.Players.B == "" && e.PlayerID != st.Players.W {
			st.Players.B = e.PlayerID
		}
		if st.Players.W != "" && st.Players.B != "" && (st.Status == "waiting" || st.Status == "") {
			st.Status = "active" // MatchStarted
		}
		return st, nil
	})

	// The whole game: validate a move against the rules engine, append it as an
	// event, fold it into new state. An illegal or out-of-turn move returns an
	// error, which Phoenix turns into a rejection sent back to the client — the
	// server is authoritative, the log stays clean.
	game.OnEvent("move", moveReducer)

	// Emit lifecycle domain events from state transitions. These are appended to
	// the log and published to the bus, so the leaderboard projection updates on
	// MatchEnded without the game loop knowing the leaderboard exists.
	game.Derive(deriveChessEvents)

	if err := game.Run(); err != nil {
		log.Fatal(err)
	}
}

func isTerminal(status string) bool {
	return status == "checkmate" || status == "stalemate" || status == "draw"
}

// deriveChessEvents turns a chess state transition into domain events:
// waiting->active emits MatchStarted; reaching a terminal status emits
// MatchEnded with the winner and seats (the leaderboard consumes this).
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
		pl, _ := phoenix.NewPayload(map[string]any{
			"winner":  n.Winner,
			"players": n.Players,
			"reason":  n.Status,
		})
		out = append(out, phoenix.DerivedEvent{Type: "MatchEnded", Payload: pl})
	}
	return out
}

func moveReducer(s any, e phoenix.Event) (any, error) {
	st := s.(chessState)
	if st.Status != "active" && st.Status != "check" {
		return st, errors.New("game is not active")
	}

	// Whose move is this, and is it their turn?
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

	var mv struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Promotion string `json:"promotion"`
	}
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

	// Apply: copy the history slice so prior state is never mutated.
	st.FEN = g.FEN()
	st.Turn = g.Turn()
	st.LastMove = &segment{From: mv.From, To: mv.To}
	st.History = append(append([]string{}, st.History...), san)

	switch g.Status() {
	case "checkmate":
		st.Status = "checkmate"
		st.Winner = seat // the side that just moved delivered mate
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
