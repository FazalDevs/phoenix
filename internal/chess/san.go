package chess

import "strings"

// pieceSANLetter returns the SAN letter for a piece kind ("" for pawns).
func pieceSANLetter(p piece) string {
	switch p.kind() {
	case wKnight:
		return "N"
	case wBishop:
		return "B"
	case wRook:
		return "R"
	case wQueen:
		return "Q"
	case wKing:
		return "K"
	}
	return ""
}

// promoSANLetter returns the uppercase letter for a promotion piece.
func promoSANLetter(p piece) string {
	switch p.kind() {
	case wQueen:
		return "Q"
	case wRook:
		return "R"
	case wBishop:
		return "B"
	case wKnight:
		return "N"
	}
	return "Q"
}

// san builds the Standard Algebraic Notation for pseudo-move m in position g
// (before the move is made). It appends "+"/"#" by examining the resulting
// position, and resolves disambiguation against the legal move list.
func (g *Game) san(m pseudoMove) string {
	mover := g.board[m.from]
	var sb strings.Builder

	if m.isCastle {
		if fileOf(m.to) == 6 {
			sb.WriteString("O-O")
		} else {
			sb.WriteString("O-O-O")
		}
	} else if mover.kind() == wPawn {
		// Pawn moves: captures include the origin file.
		isCapture := m.isEP || g.board[m.to] != empty
		if isCapture {
			sb.WriteByte(byte('a' + fileOf(m.from)))
			sb.WriteByte('x')
		}
		sb.WriteString(squareName(m.to))
		if m.promo != empty {
			sb.WriteByte('=')
			sb.WriteString(promoSANLetter(m.promo))
		}
	} else {
		// Piece moves.
		sb.WriteString(pieceSANLetter(mover))
		sb.WriteString(g.disambiguation(m, mover))
		if g.board[m.to] != empty {
			sb.WriteByte('x')
		}
		sb.WriteString(squareName(m.to))
	}

	// Determine check / checkmate by examining the resulting position.
	cp := g.clone()
	cp.applyPseudo(m)
	if cp.InCheck() {
		if len(cp.legalMoves()) == 0 {
			sb.WriteByte('#')
		} else {
			sb.WriteByte('+')
		}
	}

	return sb.String()
}

// disambiguation returns the minimal disambiguation string (file, rank, or
// both) needed when more than one piece of the same kind can move to m.to.
func (g *Game) disambiguation(m pseudoMove, mover piece) string {
	// Find other legal moves by a same-kind, same-color piece to the same
	// destination.
	var others []int // origin squares of ambiguous movers
	for _, cand := range g.legalMoves() {
		if cand.to != m.to || cand.from == m.from {
			continue
		}
		p := g.board[cand.from]
		if p.kind() == mover.kind() && p.colorOf() == mover.colorOf() {
			others = append(others, cand.from)
		}
	}
	if len(others) == 0 {
		return ""
	}

	sameFile, sameRank := false, false
	for _, o := range others {
		if fileOf(o) == fileOf(m.from) {
			sameFile = true
		}
		if rankOf(o) == rankOf(m.from) {
			sameRank = true
		}
	}

	switch {
	case !sameFile:
		// File alone disambiguates.
		return string(rune('a' + fileOf(m.from)))
	case !sameRank:
		// File is shared but rank is unique.
		return string(rune('1' + rankOf(m.from)))
	default:
		// Need both file and rank.
		return squareName(m.from)
	}
}
