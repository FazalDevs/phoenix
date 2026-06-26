// Command seed populates a running Phoenix chess backend with demo data: it
// logs in two guests, pairs them into a room via matchmaking, and plays a full
// Scholar's-mate game over real WebSockets. The result is a completed match in
// the event log — perfect for demoing the replay scrubber and event inspector.
//
//	go run ./cmd/seed                       # http://localhost:8090
//	go run ./cmd/seed -api http://localhost:8090 -games 2
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

// Scholar's mate: white mates on move 4. Each entry is (whoMoves, from, to).
var scholarsMate = []struct {
	side, from, to string
}{
	{"w", "e2", "e4"},
	{"b", "e7", "e5"},
	{"w", "f1", "c4"},
	{"b", "b8", "c6"},
	{"w", "d1", "h5"},
	{"b", "g8", "f6"},
	{"w", "h5", "f7"}, // Qxf7#
}

func main() {
	api := flag.String("api", "http://localhost:8090", "Phoenix API base URL")
	games := flag.Int("games", 1, "number of demo games to seed")
	flag.Parse()

	for i := 0; i < *games; i++ {
		if err := playGame(*api, i+1); err != nil {
			log.Fatalf("game %d: %v", i+1, err)
		}
	}
	log.Printf("seeded %d game(s). Open the admin dashboard to inspect and replay.", *games)
}

func playGame(api string, n int) error {
	white := login(api, fmt.Sprintf("DemoWhite-%d", n))
	black := login(api, fmt.Sprintf("DemoBlack-%d", n))

	// White quick-matches (creates+waits); black quick-matches (paired in).
	room := matchmake(api, white.token)
	room2 := matchmake(api, black.token)
	if room != room2 {
		return fmt.Errorf("expected pairing into the same room, got %s and %s", room, room2)
	}
	log.Printf("game %d: room %s (%s vs %s)", n, room[:8], white.id[:8], black.id[:8])

	wc := dial(api, room, white.token)
	defer wc.Close(websocket.StatusNormalClosure, "")
	bc := dial(api, room, black.token)
	defer bc.Close(websocket.StatusNormalClosure, "")
	// Drain incoming messages so the sockets stay healthy.
	go drain(wc)
	go drain(bc)
	time.Sleep(300 * time.Millisecond) // let both PlayerJoined events settle

	for _, m := range scholarsMate {
		c := wc
		if m.side == "b" {
			c = bc
		}
		msg := fmt.Sprintf(`{"type":"move","payload":{"from":%q,"to":%q}}`, m.from, m.to)
		if err := c.Write(context.Background(), websocket.MessageText, []byte(msg)); err != nil {
			return err
		}
		log.Printf("  %s %s-%s", m.side, m.from, m.to)
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

type sess struct{ id, token string }

func login(api, name string) sess {
	var out struct {
		User   struct{ ID string } `json:"user"`
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	post(api+"/login", "", fmt.Sprintf(`{"mode":"guest","display_name":%q}`, name), &out)
	return sess{id: out.User.ID, token: out.Tokens.AccessToken}
}

func matchmake(api, token string) string {
	var out struct {
		Room struct{ ID string } `json:"room"`
	}
	post(api+"/matchmake?game=chess", token, "", &out)
	return out.Room.ID
}

func dial(api, room, token string) *websocket.Conn {
	wsURL := strings.Replace(api, "http", "ws", 1) + "/ws?room=" + room + "&token=" + token
	c, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	return c
}

func drain(c *websocket.Conn) {
	for {
		if _, _, err := c.Read(context.Background()); err != nil {
			return
		}
	}
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
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			log.Fatalf("decode %s: %v (%s)", url, err, data)
		}
	}
}
