package chess

import "fmt"

// Move applies the move from->to (with optional promotion) for the side to
// move. It returns the SAN of the move on success, or an error if the move is
// illegal. On error the game is left unchanged.
//
// promo is "" for non-promotions, or one of "q","r","b","n". If a pawn reaches
// the last rank and promo is "", it defaults to a queen.
func (g *Game) Move(from, to, promo string) (string, error) {
	fromSq, err := parseSquare(from)
	if err != nil {
		return "", err
	}
	toSq, err := parseSquare(to)
	if err != nil {
		return "", err
	}

	mover := g.board[fromSq]
	if mover == empty {
		return "", fmt.Errorf("chess: no piece on %s", from)
	}
	if mover.colorOf() != g.turn {
		return "", fmt.Errorf("chess: piece on %s is not the side to move", from)
	}

	// Determine the desired promotion piece, if any.
	var wantPromo piece
	isPromotion := mover.kind() == wPawn &&
		((g.turn == white && rankOf(toSq) == 7) || (g.turn == black && rankOf(toSq) == 0))
	if isPromotion {
		wantPromo, err = promoPieceFor(promo, g.turn)
		if err != nil {
			return "", err
		}
	} else if promo != "" {
		return "", fmt.Errorf("chess: promotion %q given for non-promoting move", promo)
	}

	// Find the matching legal move.
	for _, m := range g.legalMoves() {
		if m.from != fromSq || m.to != toSq {
			continue
		}
		if isPromotion {
			if m.promo != wantPromo {
				continue
			}
		}
		// Build SAN against the pre-move position, then apply.
		san := g.san(m)
		g.applyPseudo(m)
		return san, nil
	}

	return "", fmt.Errorf("chess: illegal move %s%s", from, to)
}

// promoPieceFor maps a promo string to the colored piece, defaulting to queen
// when the string is empty.
func promoPieceFor(promo string, c color) (piece, error) {
	switch promo {
	case "", "q":
		if c == white {
			return wQueen, nil
		}
		return bQueen, nil
	case "r":
		if c == white {
			return wRook, nil
		}
		return bRook, nil
	case "b":
		if c == white {
			return wBishop, nil
		}
		return bBishop, nil
	case "n":
		if c == white {
			return wKnight, nil
		}
		return bKnight, nil
	}
	return empty, fmt.Errorf("chess: invalid promotion %q", promo)
}

// LegalMoves returns every legal move for the side to move, each populated with
// From, To, Promo and SAN.
func (g *Game) LegalMoves() []Move {
	pseudo := g.legalMoves()
	out := make([]Move, 0, len(pseudo))
	for _, m := range pseudo {
		mv := Move{
			From: squareName(m.from),
			To:   squareName(m.to),
			SAN:  g.san(m),
		}
		if m.promo != empty {
			mv.Promo = promoSANLetterLower(m.promo)
		}
		out = append(out, mv)
	}
	return out
}

// promoSANLetterLower returns the lowercase promo code ("q","r","b","n").
func promoSANLetterLower(p piece) string {
	switch p.kind() {
	case wQueen:
		return "q"
	case wRook:
		return "r"
	case wBishop:
		return "b"
	case wKnight:
		return "n"
	}
	return ""
}

// Status returns the game status from the side-to-move's perspective:
//
//	"checkmate" - in check, no legal moves
//	"stalemate" - not in check, no legal moves
//	"draw"      - insufficient material (K vs K, K vs KB, K vs KN)
//	"check"     - in check with at least one legal move
//	"active"    - otherwise
//
// Draw by insufficient material is checked before check/active because such a
// position cannot be won regardless of whose turn it is.
func (g *Game) Status() string {
	hasMoves := len(g.legalMoves()) > 0
	inCheck := g.InCheck()

	if !hasMoves {
		if inCheck {
			return "checkmate"
		}
		return "stalemate"
	}

	if g.insufficientMaterial() {
		return "draw"
	}

	if inCheck {
		return "check"
	}
	return "active"
}

// insufficientMaterial reports the minimal set of dead-draw material
// configurations: K vs K, K vs K+B, K vs K+N. Any pawn, rook, queen, or more
// than one minor piece total means a win is still theoretically possible (for
// the purposes required here).
func (g *Game) insufficientMaterial() bool {
	minors := 0 // bishops + knights (either color)
	for s := 0; s < 64; s++ {
		switch g.board[s].kind() {
		case empty, wKing:
			// kings and empties are fine
		case wBishop, wKnight:
			minors++
		default:
			// pawn, rook, or queen present -> sufficient material
			return false
		}
	}
	// Only kings (minors==0) or a single minor piece on the board.
	return minors <= 1
}
