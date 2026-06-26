// Command phoenix is the reference Phoenix server. It boots the full platform
// with a tiny generic game so the backend and dashboard are usable on their
// own. A real game would replace these reducers with its own rules — see the
// chess demo (later) for that pattern.
package main

import (
	"encoding/json"
	"log"

	"github.com/fazal/phoenix/pkg/phoenix"
)

func main() {
	game := phoenix.New(phoenix.WithGameType("sandbox"))

	// Starting state: an empty bag of per-player data plus an event counter.
	game.InitialState(func() any {
		return map[string]any{"players": map[string]any{}, "events": 0}
	})

	game.OnJoin(func(p phoenix.Player) {
		log.Printf("join: %s (%s) -> room %s", p.DisplayName, p.ID, p.RoomID)
	})
	game.OnLeave(func(p phoenix.Player) {
		log.Printf("leave: %s -> room %s", p.DisplayName, p.RoomID)
	})

	// Generic reducers so the reference server accepts common events. Each folds
	// the event payload into per-player state — the Move -> Event -> Reducer ->
	// New State loop, with no game-specific logic.
	game.OnEvent("move", genericReduce("move"))
	game.OnEvent("chat", genericReduce("chat"))
	game.OnEvent("action", genericReduce("action"))

	if err := game.Run(); err != nil {
		log.Fatal(err)
	}
}

// genericReduce stores the latest payload for an event type under the actor and
// bumps the event counter. Demonstrates immutable-ish state evolution.
func genericReduce(kind string) phoenix.Reducer {
	return func(s any, e phoenix.Event) (any, error) {
		st, _ := s.(map[string]any)
		if st == nil {
			st = map[string]any{}
		}
		players, _ := st["players"].(map[string]any)
		if players == nil {
			players = map[string]any{}
		}
		pdata, _ := players[e.PlayerID].(map[string]any)
		if pdata == nil {
			pdata = map[string]any{}
		}
		var payload any
		_ = json.Unmarshal(e.Payload, &payload)
		pdata[kind] = payload
		players[e.PlayerID] = pdata
		st["players"] = players
		if n, ok := st["events"].(int); ok {
			st["events"] = n + 1
		} else {
			st["events"] = 1
		}
		return st, nil
	}
}
