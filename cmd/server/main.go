// Command server is the deployable Phoenix backend: one process hosting multiple
// games on the same platform. It registers chess (turn-based) and arena
// (real-time) and runs them side by side — the clearest demonstration that
// Phoenix is a reusable backend, not a single game.
package main

import (
	"log"

	arena "github.com/fazal/phoenix/internal/games/arena"
	chessgame "github.com/fazal/phoenix/internal/games/chess"
	"github.com/fazal/phoenix/pkg/phoenix"
)

func main() {
	app := phoenix.New()
	chessgame.Register(app) // turn-based, event-sourced, replayable
	arena.Register(app)     // real-time, high event throughput

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
