// Command phoenix is a minimal generic server: a "sandbox" game that accepts a
// few event types and folds their payloads into per-player state. Useful for
// smoke-testing the platform without real game rules. The real demo backend is
// cmd/server (chess + arena).
package main

import (
	"encoding/json"
	"log"

	"github.com/fazal/phoenix/pkg/phoenix"
)

func main() {
	app := phoenix.New()
	app.Game("sandbox").
		InitialState(func() any { return map[string]any{"players": map[string]any{}, "events": 0} }).
		OnEvent("move", generic("move")).
		OnEvent("chat", generic("chat")).
		OnEvent("action", generic("action"))

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func generic(kind string) phoenix.Reducer {
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
