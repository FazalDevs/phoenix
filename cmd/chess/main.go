// Command chess is a single-game Phoenix backend (chess only). The deployable
// multi-game server is cmd/server. This stays as a minimal example of hosting
// one game on the SDK.
package main

import (
	"log"

	chessgame "github.com/fazal/phoenix/internal/games/chess"
	"github.com/fazal/phoenix/pkg/phoenix"
)

func main() {
	app := phoenix.New()
	chessgame.Register(app)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
