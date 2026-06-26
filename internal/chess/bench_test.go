package chess

import "testing"

// BenchmarkLegalMoveGen measures full legal move generation from the start
// position (includes king-safety filtering).
func BenchmarkLegalMoveGen(b *testing.B) {
	g := NewGame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.LegalMoves()
	}
}

// BenchmarkValidateMove mirrors what the chess reducer does on every move:
// parse a FEN and apply+validate a move against the rules.
func BenchmarkValidateMove(b *testing.B) {
	const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g, _ := Load(startFEN)
		_, _ = g.Move("e2", "e4", "")
	}
}
