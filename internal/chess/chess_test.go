package chess

import (
	"sort"
	"testing"
)

// mustMove applies a move and fails the test if it is illegal.
func mustMove(t *testing.T, g *Game, from, to, promo string) string {
	t.Helper()
	san, err := g.Move(from, to, promo)
	if err != nil {
		t.Fatalf("move %s%s%s failed: %v", from, to, promo, err)
	}
	return san
}

// Requirement 1: starting position has exactly 20 legal moves; after 1.e4 black
// has 20.
func TestStartingMoveCount(t *testing.T) {
	g := NewGame()
	if got := len(g.LegalMoves()); got != 20 {
		t.Fatalf("starting position: want 20 legal moves, got %d", got)
	}
	if g.Turn() != "w" {
		t.Fatalf("starting turn: want w, got %s", g.Turn())
	}
	mustMove(t, g, "e2", "e4", "")
	if g.Turn() != "b" {
		t.Fatalf("after 1.e4 turn: want b, got %s", g.Turn())
	}
	if got := len(g.LegalMoves()); got != 20 {
		t.Fatalf("after 1.e4: want 20 legal moves for black, got %d", got)
	}
}

// Requirement 2: Scholar's mate ends in checkmate.
func TestScholarsMate(t *testing.T) {
	g := NewGame()
	mustMove(t, g, "e2", "e4", "")        // 1. e4
	mustMove(t, g, "e7", "e5", "")        //    e5
	mustMove(t, g, "f1", "c4", "")        // 2. Bc4
	mustMove(t, g, "b8", "c6", "")        //    Nc6
	mustMove(t, g, "d1", "h5", "")        // 3. Qh5
	mustMove(t, g, "g8", "f6", "")        //    Nf6??
	san := mustMove(t, g, "h5", "f7", "") // 4. Qxf7#

	if san != "Qxf7#" {
		t.Fatalf("scholar's mate SAN: want Qxf7#, got %q", san)
	}
	if st := g.Status(); st != "checkmate" {
		t.Fatalf("scholar's mate: want checkmate, got %q", st)
	}
	if len(g.LegalMoves()) != 0 {
		t.Fatalf("scholar's mate: expected no legal moves, got %d", len(g.LegalMoves()))
	}
}

// Requirement 3: en-passant capture works and updates the board/FEN.
func TestEnPassant(t *testing.T) {
	// White pawn on e5, black to move. Black plays d7-d5, creating an ep target
	// on d6; white captures e5xd6 en passant.
	g, err := Load("4k3/3p4/8/4P3/8/8/8/4K3 b - - 0 1")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	mustMove(t, g, "d7", "d5", "") // black double push
	if g.FEN() != "4k3/8/8/3pP3/8/8/8/4K3 w - d6 0 2" {
		t.Fatalf("after d7-d5 FEN wrong: %s", g.FEN())
	}
	san := mustMove(t, g, "e5", "d6", "") // en-passant capture
	if san != "exd6" {
		t.Fatalf("en-passant SAN: want exd6, got %q", san)
	}
	// After ep, white pawn on d6, black d-pawn gone.
	want := "4k3/8/3P4/8/8/8/8/4K3 b - - 0 2"
	if g.FEN() != want {
		t.Fatalf("after en-passant FEN: want %s, got %s", want, g.FEN())
	}
}

// Requirement 4: castling kingside and queenside; castling through check
// rejected.
func TestCastling(t *testing.T) {
	// Cleared back ranks except kings and rooks: white can castle both ways.
	g, err := Load("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	san := mustMove(t, g, "e1", "g1", "") // O-O
	if san != "O-O" {
		t.Fatalf("kingside castle SAN: want O-O, got %q", san)
	}
	// Verify rook moved to f1 and king to g1.
	if g.board[sq(6, 0)] != wKing || g.board[sq(5, 0)] != wRook {
		t.Fatalf("kingside castle did not place pieces correctly: %s", g.FEN())
	}

	// Queenside in a fresh position.
	g2, _ := Load("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1")
	san = mustMove(t, g2, "e1", "c1", "") // O-O-O
	if san != "O-O-O" {
		t.Fatalf("queenside castle SAN: want O-O-O, got %q", san)
	}
	if g2.board[sq(2, 0)] != wKing || g2.board[sq(3, 0)] != wRook {
		t.Fatalf("queenside castle did not place pieces correctly: %s", g2.FEN())
	}

	// Castling through check: black rook on f8 attacks f1, so white may not
	// castle kingside (king passes through f1).
	g3, _ := Load("r4rk1/8/8/8/8/8/8/R3K2R w KQ - 0 1")
	if _, err := g3.Move("e1", "g1", ""); err == nil {
		t.Fatalf("expected kingside castle through check to be rejected")
	}
	// Confirm O-O is absent from legal moves.
	for _, m := range g3.LegalMoves() {
		if m.SAN == "O-O" {
			t.Fatalf("O-O should not be legal when passing through check")
		}
	}
}

// Requirement 5: promotion to queen and to knight; SAN shows =Q / =N.
func TestPromotion(t *testing.T) {
	// White pawn on a7 ready to promote.
	g, _ := Load("4k3/P7/8/8/8/8/8/4K3 w - - 0 1")
	san := mustMove(t, g, "a7", "a8", "q")
	if san != "a8=Q+" { // promoting to queen gives check to the black king on e8? No.
		// a8=Q does not check the king on e8 (not on same rank/file/diag through a8->e8 is same rank!).
		// a8 and e8 are on rank 8 -> queen checks along rank 8. So it's a8=Q+.
		t.Fatalf("queen promotion SAN: want a8=Q+, got %q", san)
	}
	if g.board[sq(0, 7)] != wQueen {
		t.Fatalf("promotion to queen failed: %s", g.FEN())
	}

	// Knight promotion (default-less, explicit "n").
	g2, _ := Load("4k3/P7/8/8/8/8/8/4K3 w - - 0 1")
	san = mustMove(t, g2, "a7", "a8", "n")
	if san != "a8=N" {
		t.Fatalf("knight promotion SAN: want a8=N, got %q", san)
	}
	if g2.board[sq(0, 7)] != wKnight {
		t.Fatalf("promotion to knight failed: %s", g2.FEN())
	}

	// Default promotion ("" -> queen).
	g3, _ := Load("4k3/P7/8/8/8/8/8/4K3 w - - 0 1")
	san = mustMove(t, g3, "a7", "a8", "")
	if g3.board[sq(0, 7)] != wQueen {
		t.Fatalf("default promotion should be queen: %s", g3.FEN())
	}
	if san != "a8=Q+" {
		t.Fatalf("default promotion SAN: want a8=Q+, got %q", san)
	}
}

// Requirement 6: a pinned piece cannot move.
func TestPin(t *testing.T) {
	// White king e1, white knight e2, black rook e8: the knight is pinned along
	// the e-file and cannot move.
	g, _ := Load("4r3/8/8/8/8/8/4N3/4K3 w - - 0 1")
	if _, err := g.Move("e2", "c3", ""); err == nil {
		t.Fatalf("pinned knight should not be able to move")
	}
	for _, m := range g.LegalMoves() {
		if m.From == "e2" {
			t.Fatalf("pinned knight move %s appeared in LegalMoves", m.SAN)
		}
	}
}

// Requirement 7: a known stalemate position returns "stalemate".
func TestStalemate(t *testing.T) {
	// Classic: black king a8, white king c7, white queen b6. Black to move,
	// not in check, no legal moves.
	g, _ := Load("k7/2K5/1Q6/8/8/8/8/8 b - - 0 1")
	if g.InCheck() {
		t.Fatalf("stalemate position should not be check")
	}
	if got := len(g.LegalMoves()); got != 0 {
		t.Fatalf("stalemate: expected 0 legal moves, got %d", got)
	}
	if st := g.Status(); st != "stalemate" {
		t.Fatalf("status: want stalemate, got %q", st)
	}
}

// Requirement 8: FEN round-trips.
func TestFENRoundTrip(t *testing.T) {
	g := NewGame()
	if loadFenHolder(g.FEN()).FENMust() != g.FEN() {
		t.Fatalf("starting FEN does not round-trip")
	}

	mustMove(t, g, "e2", "e4", "")
	mustMove(t, g, "c7", "c5", "")
	mustMove(t, g, "g1", "f3", "")

	reloaded, err := Load(g.FEN())
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if reloaded.FEN() != g.FEN() {
		t.Fatalf("FEN mismatch after reload:\n got %s\nwant %s", reloaded.FEN(), g.FEN())
	}
	if reloaded.Turn() != g.Turn() {
		t.Fatalf("turn mismatch after reload")
	}
	if !sameMoveSets(g.LegalMoves(), reloaded.LegalMoves()) {
		t.Fatalf("legal move sets differ after reload")
	}
}

// --- Additional coverage ---------------------------------------------------

func TestInsufficientMaterialDraw(t *testing.T) {
	cases := []string{
		"4k3/8/8/8/8/8/8/4K3 w - - 0 1",  // K vs K
		"4k3/8/8/8/8/8/8/3BK3 w - - 0 1", // K+B vs K
		"4k3/8/8/8/8/8/8/3NK3 w - - 0 1", // K+N vs K
		"4kn2/8/8/8/8/8/8/4K3 b - - 0 1", // K vs K+N
	}
	for _, fen := range cases {
		g, err := Load(fen)
		if err != nil {
			t.Fatalf("load %s: %v", fen, err)
		}
		if st := g.Status(); st != "draw" {
			t.Fatalf("FEN %s: want draw, got %q", fen, st)
		}
	}

	// K+R vs K is NOT an automatic draw.
	g, _ := Load("4k3/8/8/8/8/8/8/3RK3 w - - 0 1")
	if g.Status() == "draw" {
		t.Fatalf("K+R vs K should not be insufficient-material draw")
	}
}

func TestCheckStatusAndSAN(t *testing.T) {
	g := NewGame()
	mustMove(t, g, "e2", "e4", "")
	mustMove(t, g, "f7", "f6", "")
	mustMove(t, g, "d1", "h5", "") // Qh5+ checks the black king
	if st := g.Status(); st != "check" {
		t.Fatalf("want check, got %q", st)
	}
	if !g.InCheck() {
		t.Fatalf("InCheck should be true")
	}
}

func TestDisambiguationSAN(t *testing.T) {
	// Two white knights (b1 and f3) can both reach d2; SAN should disambiguate
	// by file: Nbd2 / Nfd2.
	g, _ := Load("4k3/8/8/8/8/5N2/8/1N5K w - - 0 1")
	var sans []string
	for _, m := range g.LegalMoves() {
		if m.To == "d2" {
			sans = append(sans, m.SAN)
		}
	}
	sort.Strings(sans)
	if len(sans) != 2 || sans[0] != "Nbd2" || sans[1] != "Nfd2" {
		t.Fatalf("knight disambiguation: want [Nbd2 Nfd2], got %v", sans)
	}
}

func TestIllegalMoveLeavesGameUnchanged(t *testing.T) {
	g := NewGame()
	before := g.FEN()
	if _, err := g.Move("e2", "e5", ""); err == nil {
		t.Fatalf("e2-e5 should be illegal")
	}
	if g.FEN() != before {
		t.Fatalf("illegal move mutated the game")
	}
}

// perft counts leaf nodes at the given depth. It is the gold-standard
// correctness check for move generation (castling, ep, promotion, pins all
// must be handled correctly for these counts to match).
func perft(g *Game, depth int) int64 {
	if depth == 0 {
		return 1
	}
	var nodes int64
	for _, m := range g.legalMoves() {
		cp := g.clone()
		cp.applyPseudo(m)
		nodes += perft(cp, depth-1)
	}
	return nodes
}

func TestPerftStartPosition(t *testing.T) {
	g := NewGame()
	// Known perft values for the initial position.
	want := []int64{1, 20, 400, 8902, 197281}
	for d := 1; d <= 4; d++ {
		if got := perft(g, d); got != want[d] {
			t.Fatalf("perft(start, %d): want %d, got %d", d, want[d], got)
		}
	}
}

func TestPerftKiwipete(t *testing.T) {
	// "Kiwipete" position - rich in castling, ep and promotions; a classic
	// movegen stress test.
	g, err := Load("r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1")
	if err != nil {
		t.Fatalf("load kiwipete: %v", err)
	}
	if got := perft(g, 1); got != 48 {
		t.Fatalf("perft(kiwipete,1): want 48, got %d", got)
	}
	if got := perft(g, 2); got != 2039 {
		t.Fatalf("perft(kiwipete,2): want 2039, got %d", got)
	}
	if got := perft(g, 3); got != 97862 {
		t.Fatalf("perft(kiwipete,3): want 97862, got %d", got)
	}
}

// --- helpers ---------------------------------------------------------------

// fenHolder lets the round-trip test read FEN from a *Game returned by Load
// without ignoring the error inline. We wrap via a tiny helper type.
type fenHolder struct{ g *Game }

func (h fenHolder) FENMust() string { return h.g.FEN() }

// Load shim used in TestFENRoundTrip to chain Load(...).FENMust().
func loadFenHolder(fen string) fenHolder {
	g, err := Load(fen)
	if err != nil {
		panic(err)
	}
	return fenHolder{g}
}

func sameMoveSets(a, b []Move) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(m Move) string { return m.From + m.To + m.Promo }
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i := range a {
		as[i] = key(a[i])
		bs[i] = key(b[i])
	}
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
