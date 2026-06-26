package chess

// This file implements attack detection and move generation.

// Direction offsets expressed as (file delta, rank delta).
var (
	knightDeltas = [8][2]int{
		{1, 2}, {2, 1}, {2, -1}, {1, -2},
		{-1, -2}, {-2, -1}, {-2, 1}, {-1, 2},
	}
	kingDeltas = [8][2]int{
		{1, 0}, {1, 1}, {0, 1}, {-1, 1},
		{-1, 0}, {-1, -1}, {0, -1}, {1, -1},
	}
	bishopDirs = [4][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}}
	rookDirs   = [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
)

// isSquareAttacked reports whether square s is attacked by any piece of color
// `by`. This drives check detection and castling legality.
func (g *Game) isSquareAttacked(s int, by color) bool {
	f, r := fileOf(s), rankOf(s)

	// Pawn attacks. A white pawn on (f-1,r-1) or (f+1,r-1) attacks s; a black
	// pawn on (f-1,r+1) or (f+1,r+1) attacks s.
	if by == white {
		for _, df := range []int{-1, 1} {
			af, ar := f+df, r-1
			if onBoard(af, ar) && g.board[sq(af, ar)] == wPawn {
				return true
			}
		}
	} else {
		for _, df := range []int{-1, 1} {
			af, ar := f+df, r+1
			if onBoard(af, ar) && g.board[sq(af, ar)] == bPawn {
				return true
			}
		}
	}

	// Knight attacks.
	wantKnight := wKnight
	if by == black {
		wantKnight = bKnight
	}
	for _, d := range knightDeltas {
		af, ar := f+d[0], r+d[1]
		if onBoard(af, ar) && g.board[sq(af, ar)] == wantKnight {
			return true
		}
	}

	// King attacks (adjacent squares).
	wantKing := wKing
	if by == black {
		wantKing = bKing
	}
	for _, d := range kingDeltas {
		af, ar := f+d[0], r+d[1]
		if onBoard(af, ar) && g.board[sq(af, ar)] == wantKing {
			return true
		}
	}

	// Sliding attacks along bishop diagonals (bishop or queen).
	if g.slidingAttack(f, r, bishopDirs[:], by, false) {
		return true
	}
	// Sliding attacks along rook lines (rook or queen).
	if g.slidingAttack(f, r, rookDirs[:], by, true) {
		return true
	}

	return false
}

// slidingAttack walks each direction from (f,r) until it hits a piece. If that
// piece belongs to `by` and is the appropriate sliding type (queen always, plus
// rook when straight==true, else bishop), it is an attacker.
func (g *Game) slidingAttack(f, r int, dirs [][2]int, by color, straight bool) bool {
	var lineP, queenP piece
	if by == white {
		queenP = wQueen
		if straight {
			lineP = wRook
		} else {
			lineP = wBishop
		}
	} else {
		queenP = bQueen
		if straight {
			lineP = bRook
		} else {
			lineP = bBishop
		}
	}
	for _, d := range dirs {
		af, ar := f+d[0], r+d[1]
		for onBoard(af, ar) {
			p := g.board[sq(af, ar)]
			if p != empty {
				if p == lineP || p == queenP {
					return true
				}
				break // blocked by some other piece
			}
			af += d[0]
			ar += d[1]
		}
	}
	return false
}

// InCheck reports whether the side to move is currently in check.
func (g *Game) InCheck() bool {
	k := g.findKing(g.turn)
	if k < 0 {
		return false
	}
	return g.isSquareAttacked(k, g.turn.opp())
}

// pseudoMove is an internal candidate move before legality (king-safety)
// filtering. It carries enough information to apply and to build SAN.
type pseudoMove struct {
	from, to int
	promo    piece // empty unless a promotion
	isEP     bool  // en-passant capture
	isCastle bool  // castling move
}

// generatePseudoMoves produces all pseudo-legal moves for the side to move
// (i.e. moves that obey piece movement but may leave the king in check).
func (g *Game) generatePseudoMoves() []pseudoMove {
	moves := make([]pseudoMove, 0, 48)
	c := g.turn
	for s := 0; s < 64; s++ {
		p := g.board[s]
		if p == empty || p.colorOf() != c {
			continue
		}
		switch p.kind() {
		case wPawn:
			g.genPawn(s, c, &moves)
		case wKnight:
			g.genStep(s, c, knightDeltas[:], &moves)
		case wKing:
			g.genStep(s, c, kingDeltas[:], &moves)
			g.genCastle(s, c, &moves)
		case wBishop:
			g.genSlide(s, c, bishopDirs[:], &moves)
		case wRook:
			g.genSlide(s, c, rookDirs[:], &moves)
		case wQueen:
			g.genSlide(s, c, bishopDirs[:], &moves)
			g.genSlide(s, c, rookDirs[:], &moves)
		}
	}
	return moves
}

// genStep handles non-sliding pieces (knight, king single steps).
func (g *Game) genStep(s int, c color, deltas [][2]int, out *[]pseudoMove) {
	f, r := fileOf(s), rankOf(s)
	for _, d := range deltas {
		af, ar := f+d[0], r+d[1]
		if !onBoard(af, ar) {
			continue
		}
		dst := sq(af, ar)
		t := g.board[dst]
		if t == empty || t.colorOf() != c {
			*out = append(*out, pseudoMove{from: s, to: dst})
		}
	}
}

// genSlide handles sliding pieces (bishop, rook, queen).
func (g *Game) genSlide(s int, c color, dirs [][2]int, out *[]pseudoMove) {
	f, r := fileOf(s), rankOf(s)
	for _, d := range dirs {
		af, ar := f+d[0], r+d[1]
		for onBoard(af, ar) {
			dst := sq(af, ar)
			t := g.board[dst]
			if t == empty {
				*out = append(*out, pseudoMove{from: s, to: dst})
			} else {
				if t.colorOf() != c {
					*out = append(*out, pseudoMove{from: s, to: dst})
				}
				break
			}
			af += d[0]
			ar += d[1]
		}
	}
}

// promoPieces returns the four promotion piece options for a color.
func promoPieces(c color) [4]piece {
	if c == white {
		return [4]piece{wQueen, wRook, wBishop, wKnight}
	}
	return [4]piece{bQueen, bRook, bBishop, bKnight}
}

// genPawn handles pawn pushes, captures, en-passant and promotion.
func (g *Game) genPawn(s int, c color, out *[]pseudoMove) {
	f, r := fileOf(s), rankOf(s)
	var dir, startRank, promoRank int
	if c == white {
		dir, startRank, promoRank = 1, 1, 7
	} else {
		dir, startRank, promoRank = -1, 6, 0
	}

	// Single push.
	oneR := r + dir
	if onBoard(f, oneR) && g.board[sq(f, oneR)] == empty {
		if oneR == promoRank {
			for _, pp := range promoPieces(c) {
				*out = append(*out, pseudoMove{from: s, to: sq(f, oneR), promo: pp})
			}
		} else {
			*out = append(*out, pseudoMove{from: s, to: sq(f, oneR)})
			// Double push from the starting rank.
			if r == startRank {
				twoR := r + 2*dir
				if g.board[sq(f, twoR)] == empty {
					*out = append(*out, pseudoMove{from: s, to: sq(f, twoR)})
				}
			}
		}
	}

	// Captures (including promotion captures and en-passant).
	for _, df := range []int{-1, 1} {
		af, ar := f+df, r+dir
		if !onBoard(af, ar) {
			continue
		}
		dst := sq(af, ar)
		t := g.board[dst]
		if t != empty && t.colorOf() != c {
			if ar == promoRank {
				for _, pp := range promoPieces(c) {
					*out = append(*out, pseudoMove{from: s, to: dst, promo: pp})
				}
			} else {
				*out = append(*out, pseudoMove{from: s, to: dst})
			}
		} else if dst == g.epSquare && g.epSquare >= 0 {
			// En-passant capture: destination is the ep target square.
			*out = append(*out, pseudoMove{from: s, to: dst, isEP: true})
		}
	}
}

// genCastle adds castling pseudo-moves when rights and emptiness permit. The
// "not through check" rule is enforced here too (it is cheap and avoids
// generating illegal castles).
func (g *Game) genCastle(s int, c color, out *[]pseudoMove) {
	opp := c.opp()
	if c == white {
		if s != sq(4, 0) { // king must be on e1
			return
		}
		// Kingside: squares f1,g1 empty; e1,f1,g1 not attacked; rook on h1.
		if g.castle&castleWK != 0 &&
			g.board[sq(5, 0)] == empty && g.board[sq(6, 0)] == empty &&
			g.board[sq(7, 0)] == wRook &&
			!g.isSquareAttacked(sq(4, 0), opp) &&
			!g.isSquareAttacked(sq(5, 0), opp) &&
			!g.isSquareAttacked(sq(6, 0), opp) {
			*out = append(*out, pseudoMove{from: s, to: sq(6, 0), isCastle: true})
		}
		// Queenside: b1,c1,d1 empty; e1,d1,c1 not attacked; rook on a1.
		if g.castle&castleWQ != 0 &&
			g.board[sq(1, 0)] == empty && g.board[sq(2, 0)] == empty && g.board[sq(3, 0)] == empty &&
			g.board[sq(0, 0)] == wRook &&
			!g.isSquareAttacked(sq(4, 0), opp) &&
			!g.isSquareAttacked(sq(3, 0), opp) &&
			!g.isSquareAttacked(sq(2, 0), opp) {
			*out = append(*out, pseudoMove{from: s, to: sq(2, 0), isCastle: true})
		}
	} else {
		if s != sq(4, 7) { // king must be on e8
			return
		}
		if g.castle&castleBK != 0 &&
			g.board[sq(5, 7)] == empty && g.board[sq(6, 7)] == empty &&
			g.board[sq(7, 7)] == bRook &&
			!g.isSquareAttacked(sq(4, 7), opp) &&
			!g.isSquareAttacked(sq(5, 7), opp) &&
			!g.isSquareAttacked(sq(6, 7), opp) {
			*out = append(*out, pseudoMove{from: s, to: sq(6, 7), isCastle: true})
		}
		if g.castle&castleBQ != 0 &&
			g.board[sq(1, 7)] == empty && g.board[sq(2, 7)] == empty && g.board[sq(3, 7)] == empty &&
			g.board[sq(0, 7)] == bRook &&
			!g.isSquareAttacked(sq(4, 7), opp) &&
			!g.isSquareAttacked(sq(3, 7), opp) &&
			!g.isSquareAttacked(sq(2, 7), opp) {
			*out = append(*out, pseudoMove{from: s, to: sq(2, 7), isCastle: true})
		}
	}
}

// applyPseudo mutates g by playing the pseudo-move m. It updates the board,
// castling rights, en-passant target, clocks, turn and fullmove counter. It does
// NOT validate legality. Used on clones for legality checks and for real moves.
func (g *Game) applyPseudo(m pseudoMove) {
	mover := g.board[m.from]
	c := mover.colorOf()
	captured := g.board[m.to]

	// Reset en-passant target; set later if this is a double pawn push.
	prevEP := g.epSquare
	g.epSquare = -1

	// Move the piece.
	g.board[m.to] = mover
	g.board[m.from] = empty

	isPawn := mover.kind() == wPawn

	// En-passant capture removes the pawn behind the destination.
	if m.isEP {
		// Captured pawn is on the same file as `to`, on the moving side's
		// origin rank.
		capRank := rankOf(m.from)
		capFile := fileOf(m.to)
		g.board[sq(capFile, capRank)] = empty
		captured = wPawn // mark as a capture for clock purposes
		_ = prevEP
	}

	// Promotion.
	if m.promo != empty {
		g.board[m.to] = m.promo
	}

	// Double pawn push sets the en-passant target square (the square jumped over).
	if isPawn {
		if rankOf(m.to)-rankOf(m.from) == 2 {
			g.epSquare = sq(fileOf(m.from), rankOf(m.from)+1)
		} else if rankOf(m.from)-rankOf(m.to) == 2 {
			g.epSquare = sq(fileOf(m.from), rankOf(m.from)-1)
		}
	}

	// Castling: move the rook to the other side of the king.
	if m.isCastle {
		switch m.to {
		case sq(6, 0): // white kingside
			g.board[sq(5, 0)] = wRook
			g.board[sq(7, 0)] = empty
		case sq(2, 0): // white queenside
			g.board[sq(3, 0)] = wRook
			g.board[sq(0, 0)] = empty
		case sq(6, 7): // black kingside
			g.board[sq(5, 7)] = bRook
			g.board[sq(7, 7)] = empty
		case sq(2, 7): // black queenside
			g.board[sq(3, 7)] = bRook
			g.board[sq(0, 7)] = empty
		}
	}

	// Update castling rights: if king or rook moves, or a rook is captured.
	g.updateCastleRights(m.from, m.to, mover)

	// Halfmove clock: reset on pawn move or capture, otherwise increment.
	if isPawn || captured != empty {
		g.halfmove = 0
	} else {
		g.halfmove++
	}

	// Fullmove number increments after black moves.
	if c == black {
		g.fullmove++
	}

	// Switch side to move.
	g.turn = c.opp()
}

// updateCastleRights revokes castling rights affected by a move's origin/target.
func (g *Game) updateCastleRights(from, to int, mover piece) {
	switch mover {
	case wKing:
		g.castle &^= castleWK | castleWQ
	case bKing:
		g.castle &^= castleBK | castleBQ
	}
	// Rook leaving its home square, or any piece landing on a rook home square
	// (capturing the rook), removes the corresponding right.
	clear := func(s int) {
		switch s {
		case sq(0, 0):
			g.castle &^= castleWQ
		case sq(7, 0):
			g.castle &^= castleWK
		case sq(0, 7):
			g.castle &^= castleBQ
		case sq(7, 7):
			g.castle &^= castleBK
		}
	}
	clear(from)
	clear(to)
}

// legalMoves returns the pseudo-moves that do not leave the mover's king in
// check. This is the core legality filter used everywhere.
func (g *Game) legalMoves() []pseudoMove {
	c := g.turn
	pseudo := g.generatePseudoMoves()
	legal := make([]pseudoMove, 0, len(pseudo))
	for _, m := range pseudo {
		cp := g.clone()
		cp.applyPseudo(m)
		// After applying, it is the opponent's turn; we must check that OUR king
		// (color c) is not attacked.
		k := cp.findKing(c)
		if k >= 0 && !cp.isSquareAttacked(k, c.opp()) {
			legal = append(legal, m)
		}
	}
	return legal
}
