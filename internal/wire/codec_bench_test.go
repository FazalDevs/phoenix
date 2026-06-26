package wire

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fazal/phoenix/internal/core"
)

// chessState mirrors the broadcast game state; its history grows with the match,
// which is exactly why full-state broadcasts get bigger over time.
func chessStateWithHistory(plies int) map[string]any {
	hist := make([]string, plies)
	sans := []string{"e4", "e5", "Nf3", "Nc6", "Bb5", "a6", "Ba4", "Nf6", "O-O", "Be7"}
	for i := range hist {
		hist[i] = sans[i%len(sans)]
	}
	return map[string]any{
		"fen":     "r1bqk2r/1pppbppp/p1n2n2/4p3/B3P3/5N2/PPPP1PPP/RNBQ1RK1 w kq - 0 6",
		"turn":    "w",
		"status":  "active",
		"players": map[string]any{"w": "a1b2c3d4-0000-4000-8000-000000000001", "b": "a1b2c3d4-0000-4000-8000-000000000002"},
		"winner":  "",
		"history": hist,
	}
}

func sampleEvent() *core.Event {
	payload, _ := json.Marshal(map[string]any{"from": "e2", "to": "e4"})
	return &core.Event{ID: "8f14e45f-ceea-467a-9e1a-2b3c4d5e6f70", Seq: 42, Type: "move",
		RoomID: "a1b2c3d4-0000-4000-8000-000000000003", PlayerID: "a1b2c3d4-0000-4000-8000-000000000001", Payload: payload}
}

// BenchmarkBroadcastEncoding reports both payload SIZE and serialize SPEED across
// the four combinations: {full-state, delta} x {JSON, MessagePack}, at several
// match lengths. The size columns are the headline: delta is constant; full-state
// grows; binary shrinks both.
func BenchmarkBroadcastEncoding(b *testing.B) {
	e := sampleEvent()
	codecs := []Codec{JSONCodec{}, MsgpackCodec{}}

	for _, plies := range []int{20, 60, 150} {
		state := chessStateWithHistory(plies)
		full := FullStateMsg{Type: "event", RoomID: e.RoomID, Event: e, State: state}
		delta := DeltaMsg{Type: "event", RoomID: e.RoomID, Event: e}

		for _, c := range codecs {
			fb, _ := c.Marshal(full)
			db, _ := c.Marshal(delta)
			b.Run(fmt.Sprintf("full-%s/%dplies", c.Name(), plies), func(b *testing.B) {
				b.SetBytes(int64(len(fb)))
				b.ReportMetric(float64(len(fb)), "payload_B")
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = c.Marshal(full)
				}
			})
			b.Run(fmt.Sprintf("delta-%s/%dplies", c.Name(), plies), func(b *testing.B) {
				b.SetBytes(int64(len(db)))
				b.ReportMetric(float64(len(db)), "payload_B")
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = c.Marshal(delta)
				}
			})
		}
	}
}
