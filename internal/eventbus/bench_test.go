package eventbus

import (
	"strconv"
	"testing"

	"github.com/fazal/phoenix/internal/core"
)

// BenchmarkPublishFanout measures the cost of publishing one event to a bus with
// several subscribers (non-blocking fan-out on the write path).
func BenchmarkPublishFanout(b *testing.B) {
	bus := NewInProcess()
	for i := 0; i < 8; i++ {
		bus.Subscribe("sub"+strconv.Itoa(i), func(core.Event) {})
	}
	e := core.Event{RoomID: "room1", Type: "move"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(e)
	}
}
