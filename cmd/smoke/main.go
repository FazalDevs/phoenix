// Command smoke is a minimal end-to-end client for the Phoenix backend, used to
// exercise and demo the realtime path without a real game: it logs in as a
// guest, creates a room, opens a WebSocket, and sends periodic "move" events.
// While it runs, the admin dashboard shows a live player and a growing event log.
//
//	go run ./cmd/smoke              # uses http://localhost:8090
//	go run ./cmd/smoke -api http://localhost:8080 -moves 20
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	api := flag.String("api", "http://localhost:8090", "Phoenix API base URL")
	moves := flag.Int("moves", 15, "number of move events to send")
	flag.Parse()

	// 1. Guest login -> access token.
	var login struct {
		User   struct{ ID string } `json:"user"`
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	post(*api+"/login", "", `{"mode":"guest","display_name":"SmokeBot"}`, &login)
	token := login.Tokens.AccessToken
	log.Printf("logged in as %s", login.User.ID)

	// 2. Create a room.
	var room struct {
		ID string `json:"id"`
	}
	post(*api+"/rooms", token, `{"game_type":"sandbox","max_players":4}`, &room)
	log.Printf("created room %s", room.ID)

	// 3. Open the WebSocket (token via query so no header needed).
	wsURL := strings.Replace(*api, "http", "ws", 1) + "/ws?room=" + room.ID + "&token=" + token
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		log.Fatalf("ws dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	log.Printf("connected: %s", wsURL)

	// Read server messages in the background (snapshot + event broadcasts).
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			log.Printf("<- %s", truncate(string(data), 160))
		}
	}()

	// 4. Send periodic move events.
	for i := 1; i <= *moves; i++ {
		msg := fmt.Sprintf(`{"type":"move","payload":{"x":%d,"y":%d}}`, i, i*2)
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			log.Fatalf("ws write: %v", err)
		}
		log.Printf("-> move %d", i)
		time.Sleep(1 * time.Second)
	}

	log.Printf("done. room=%s — open the dashboard to inspect the event log.", room.ID)
	time.Sleep(2 * time.Second)
}

func post(url, token, body string, out any) {
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Fatalf("POST %s: %d %s", url, resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		log.Fatalf("decode %s: %v (%s)", url, err, data)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
