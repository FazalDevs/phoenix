// Command loadtest is an OPEN-LOOP end-to-end WebSocket load test. Each virtual
// client owns an arena room and sends "move" intents at a fixed scheduled rate
// (independent of responses — this avoids coordinated omission). Every intent
// carries a timestamp the server echoes back in its broadcast, so we measure the
// true round-trip latency of the full path:
//
//	WS send -> gateway -> validate -> append(Postgres) -> reduce -> broadcast -> WS recv
//
// It reports achieved throughput and p50/p95/p99 latency, so you can ramp the
// offered rate and find the knee (max sustainable rate under a latency SLO).
//
//	go run ./cmd/loadtest -api http://localhost:8090 -conns 40 -rate 600 -dur 20s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func main() {
	api := flag.String("api", "http://localhost:8090", "Phoenix API base URL")
	conns := flag.Int("conns", 40, "concurrent connections (each its own room)")
	rate := flag.Int("rate", 600, "TOTAL offered events/sec across all connections")
	dur := flag.Duration("dur", 20*time.Second, "steady-state duration")
	flag.Parse()

	token := login(*api)
	perConn := float64(*rate) / float64(*conns)
	interval := time.Duration(float64(time.Second) / perConn)
	log.Printf("offered: %d ev/s across %d conns = %.1f ev/s/conn (every %s)", *rate, *conns, perConn, interval.Round(time.Millisecond))

	// Create one arena room per connection (single player per room → every
	// broadcast a client receives is its own event, so RTT matching is exact).
	rooms := make([]string, *conns)
	sem := make(chan struct{}, 16)
	var wgRooms sync.WaitGroup
	for i := range rooms {
		wgRooms.Add(1)
		go func(i int) {
			defer wgRooms.Done()
			sem <- struct{}{}
			rooms[i] = createRoom(*api, token)
			<-sem
		}(i)
	}
	wgRooms.Wait()

	var (
		sent     atomic.Int64
		recv     atomic.Int64
		writeErr atomic.Int64
		latMu    sync.Mutex
		lat      []float64
		wg       sync.WaitGroup
		start    = make(chan struct{})
	)

	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func(room string) {
			defer wg.Done()
			ctx := context.Background()
			url := strings.Replace(*api, "http", "ws", 1) + "/ws?room=" + room + "&token=" + token
			sem <- struct{}{}
			c, _, err := websocket.Dial(ctx, url, nil)
			<-sem
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")

			// Reader: match "move" echoes by the embedded timestamp.
			local := make([]float64, 0, 4096)
			go func() {
				for {
					_, data, err := c.Read(ctx)
					if err != nil {
						return
					}
					var m struct {
						Type  string `json:"type"`
						Event struct {
							Type    string `json:"type"`
							Payload struct {
								T int64 `json:"t"`
							} `json:"payload"`
						} `json:"event"`
					}
					if json.Unmarshal(data, &m) == nil && m.Type == "event" && m.Event.Type == "move" && m.Event.Payload.T > 0 {
						rttMs := float64(time.Now().UnixNano()-m.Event.Payload.T) / 1e6
						local = append(local, rttMs)
						recv.Add(1)
					}
				}
			}()

			<-start
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			deadline := time.Now().Add(*dur)
			for time.Now().Before(deadline) {
				<-ticker.C
				msg := fmt.Sprintf(`{"type":"move","payload":{"x":%d,"y":%d,"t":%d}}`,
					40+rand.Intn(920), 40+rand.Intn(560), time.Now().UnixNano())
				if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
					writeErr.Add(1)
					return
				}
				sent.Add(1)
			}
			time.Sleep(500 * time.Millisecond) // drain in-flight echoes
			latMu.Lock()
			lat = append(lat, local...)
			latMu.Unlock()
		}(rooms[i])
	}

	time.Sleep(1500 * time.Millisecond) // let dials settle
	t0 := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(t0)

	sort.Float64s(lat)
	fmt.Println("\n================ end-to-end load test ================")
	fmt.Printf("connections      : %d (1 player/room)\n", *conns)
	fmt.Printf("offered rate     : %d ev/s\n", *rate)
	fmt.Printf("duration         : %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("sent             : %d (%.0f ev/s)\n", sent.Load(), float64(sent.Load())/elapsed.Seconds())
	fmt.Printf("processed (echoed): %d (%.0f ev/s)\n", recv.Load(), float64(recv.Load())/elapsed.Seconds())
	fmt.Printf("write errors     : %d\n", writeErr.Load())
	if len(lat) > 0 {
		fmt.Printf("RTT p50/p95/p99  : %.1f / %.1f / %.1f ms\n", pct(lat, 50), pct(lat, 95), pct(lat, 99))
		fmt.Printf("RTT max          : %.1f ms\n", lat[len(lat)-1])
	}
	fmt.Println("======================================================")
}

func pct(s []float64, p int) float64 {
	if len(s) == 0 {
		return 0
	}
	i := p * len(s) / 100
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func login(api string) string {
	var out struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	post(api+"/login", "", `{"mode":"guest","display_name":"LoadBot"}`, &out)
	return out.Tokens.AccessToken
}

func createRoom(api, token string) string {
	var out struct {
		ID string `json:"id"`
	}
	post(api+"/rooms", token, `{"game_type":"arena","max_players":4}`, &out)
	return out.ID
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
		_ = json.Unmarshal(data, out)
	}
}
