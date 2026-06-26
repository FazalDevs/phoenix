// Package chess is a self-contained chess rules engine.
//
// It provides full legal move generation, FEN parsing/serialization,
// SAN (Standard Algebraic Notation) generation, and game-status detection
// (check, checkmate, stalemate and minimal insufficient-material draws).
//
// The package depends only on the Go standard library.
package chess

import (
	"errors"
	"fmt"
	"strings"
)

// Move describes a single move in a form that is convenient for callers and
// JSON serialization.
type Move struct {
	From  string `json:"from"`  // origin square, e.g. "e2"
	To    string `json:"to"`    // destination square, e.g. "e4"
	Promo string `json:"promo"` // "" or one of "q","r","b","n"
	SAN   string `json:"san"`   // Standard Algebraic Notation, e.g. "e4", "Nf3", "O-O", "exd6", "e8=Q+"
}

// --- Internal board representation -----------------------------------------
//
// The board is an 8x8 array of pieces indexed 0..63. Index 0 is a1, index 7 is
// h1, index 56 is a8, index 63 is h8. In other words:
//
//	index = rank*8 + file
//
// where file 0..7 == a..h and rank 0..7 == 1..8.

// piece is a single board square's contents. The zero value (empty) is 0.
type piece uint8

const (
	empty piece = iota
	wPawn
	wKnight
	wBishop
	wRook
	wQueen
	wKing
	bPawn
	bKnight
	bBishop
	bRook
	bQueen
	bKing
)

// color of a piece / side to move.
type color uint8

const (
	white color = iota
	black
)

func (c color) opp() color {
	if c == white {
		return black
	}
	return white
}

// isWhite reports whether p is a white piece (p must be non-empty).
func (p piece) isWhite() bool { return p >= wPawn && p <= wKing }

// isBlack reports whether p is a black piece (p must be non-empty).
func (p piece) isBlack() bool { return p >= bPawn && p <= bKing }

// colorOf returns the color of a non-empty piece.
func (p piece) colorOf() color {
	if p.isWhite() {
		return white
	}
	return black
}

// kind collapses a piece to its type, ignoring color. Returns one of the white
// piece constants (wPawn..wKing) for convenience.
func (p piece) kind() piece {
	switch p {
	case wPawn, bPawn:
		return wPawn
	case wKnight, bKnight:
		return wKnight
	case wBishop, bBishop:
		return wBishop
	case wRook, bRook:
		return wRook
	case wQueen, bQueen:
		return wQueen
	case wKing, bKing:
		return wKing
	}
	return empty
}

// castling rights bit flags.
const (
	castleWK uint8 = 1 << iota // white kingside  (O-O)
	castleWQ                   // white queenside (O-O-O)
	castleBK                   // black kingside
	castleBQ                   // black queenside
)

// Game holds the full state required to play and reason about a position.
type Game struct {
	board    [64]piece
	turn     color
	castle   uint8 // bitmask of castle* flags
	epSquare int   // en-passant target square index, or -1 if none
	halfmove int   // halfmove clock (for 50-move rule; tracked, not enforced)
	fullmove int   // fullmove number
}

// --- Square helpers --------------------------------------------------------

// sq builds a square index from file (0..7) and rank (0..7).
func sq(file, rank int) int { return rank*8 + file }

// fileOf / rankOf decompose a square index.
func fileOf(s int) int { return s % 8 }
func rankOf(s int) int { return s / 8 }

// onBoard reports whether file/rank are within 0..7.
func onBoard(file, rank int) bool { return file >= 0 && file < 8 && rank >= 0 && rank < 8 }

// squareName converts an index to algebraic, e.g. 0 -> "a1".
func squareName(s int) string {
	if s < 0 || s > 63 {
		return "-"
	}
	return string(rune('a'+fileOf(s))) + string(rune('1'+rankOf(s)))
}

// parseSquare converts algebraic ("e4") to an index, or returns an error.
func parseSquare(name string) (int, error) {
	if len(name) != 2 {
		return -1, fmt.Errorf("chess: invalid square %q", name)
	}
	f := int(name[0] - 'a')
	r := int(name[1] - '1')
	if !onBoard(f, r) {
		return -1, fmt.Errorf("chess: invalid square %q", name)
	}
	return sq(f, r), nil
}

// --- Constructors ----------------------------------------------------------

const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

// NewGame returns a game set to the standard starting position.
func NewGame() *Game {
	g, err := Load(startFEN)
	if err != nil {
		// startFEN is a constant we control; this can never fail.
		panic(err)
	}
	return g
}

// pieceFromFEN maps a FEN piece letter to an internal piece value.
func pieceFromFEN(r rune) (piece, bool) {
	switch r {
	case 'P':
		return wPawn, true
	case 'N':
		return wKnight, true
	case 'B':
		return wBishop, true
	case 'R':
		return wRook, true
	case 'Q':
		return wQueen, true
	case 'K':
		return wKing, true
	case 'p':
		return bPawn, true
	case 'n':
		return bKnight, true
	case 'b':
		return bBishop, true
	case 'r':
		return bRook, true
	case 'q':
		return bQueen, true
	case 'k':
		return bKing, true
	}
	return empty, false
}

// fenLetter maps an internal piece to its FEN letter.
func fenLetter(p piece) byte {
	switch p {
	case wPawn:
		return 'P'
	case wKnight:
		return 'N'
	case wBishop:
		return 'B'
	case wRook:
		return 'R'
	case wQueen:
		return 'Q'
	case wKing:
		return 'K'
	case bPawn:
		return 'p'
	case bKnight:
		return 'n'
	case bBishop:
		return 'b'
	case bRook:
		return 'r'
	case bQueen:
		return 'q'
	case bKing:
		return 'k'
	}
	return ' '
}

// Load constructs a Game from a FEN string.
func Load(fen string) (*Game, error) {
	fields := strings.Fields(strings.TrimSpace(fen))
	if len(fields) < 4 {
		return nil, errors.New("chess: FEN must have at least 4 fields")
	}

	g := &Game{epSquare: -1, fullmove: 1}

	// Field 1: piece placement, ranks from 8 down to 1.
	ranks := strings.Split(fields[0], "/")
	if len(ranks) != 8 {
		return nil, errors.New("chess: FEN board must have 8 ranks")
	}
	for i, rankStr := range ranks {
		rank := 7 - i // FEN starts at rank 8 (index 7)
		file := 0
		for _, r := range rankStr {
			if r >= '1' && r <= '8' {
				file += int(r - '0')
				if file > 8 {
					return nil, fmt.Errorf("chess: FEN rank %q overflows", rankStr)
				}
				continue
			}
			p, ok := pieceFromFEN(r)
			if !ok {
				return nil, fmt.Errorf("chess: invalid FEN piece %q", string(r))
			}
			if file > 7 {
				return nil, fmt.Errorf("chess: FEN rank %q overflows", rankStr)
			}
			g.board[sq(file, rank)] = p
			file++
		}
		if file != 8 {
			return nil, fmt.Errorf("chess: FEN rank %q does not fill 8 files", rankStr)
		}
	}

	// Field 2: side to move.
	switch fields[1] {
	case "w":
		g.turn = white
	case "b":
		g.turn = black
	default:
		return nil, fmt.Errorf("chess: invalid side to move %q", fields[1])
	}

	// Field 3: castling rights.
	if fields[2] != "-" {
		for _, r := range fields[2] {
			switch r {
			case 'K':
				g.castle |= castleWK
			case 'Q':
				g.castle |= castleWQ
			case 'k':
				g.castle |= castleBK
			case 'q':
				g.castle |= castleBQ
			default:
				return nil, fmt.Errorf("chess: invalid castling field %q", fields[2])
			}
		}
	}

	// Field 4: en-passant target square.
	if fields[3] != "-" {
		s, err := parseSquare(fields[3])
		if err != nil {
			return nil, fmt.Errorf("chess: invalid en-passant square %q", fields[3])
		}
		g.epSquare = s
	}

	// Field 5: halfmove clock (optional).
	if len(fields) >= 5 {
		if _, err := fmt.Sscanf(fields[4], "%d", &g.halfmove); err != nil {
			g.halfmove = 0
		}
	}
	// Field 6: fullmove number (optional).
	if len(fields) >= 6 {
		if _, err := fmt.Sscanf(fields[5], "%d", &g.fullmove); err != nil {
			g.fullmove = 1
		}
	}
	if g.fullmove < 1 {
		g.fullmove = 1
	}

	return g, nil
}

// FEN serializes the current position to a FEN string. It round-trips with Load.
func (g *Game) FEN() string {
	var sb strings.Builder

	// Field 1: board, rank 8 down to rank 1.
	for rank := 7; rank >= 0; rank-- {
		emptyCount := 0
		for file := 0; file < 8; file++ {
			p := g.board[sq(file, rank)]
			if p == empty {
				emptyCount++
				continue
			}
			if emptyCount > 0 {
				sb.WriteByte(byte('0' + emptyCount))
				emptyCount = 0
			}
			sb.WriteByte(fenLetter(p))
		}
		if emptyCount > 0 {
			sb.WriteByte(byte('0' + emptyCount))
		}
		if rank > 0 {
			sb.WriteByte('/')
		}
	}

	// Field 2: side to move.
	sb.WriteByte(' ')
	if g.turn == white {
		sb.WriteByte('w')
	} else {
		sb.WriteByte('b')
	}

	// Field 3: castling rights.
	sb.WriteByte(' ')
	if g.castle == 0 {
		sb.WriteByte('-')
	} else {
		if g.castle&castleWK != 0 {
			sb.WriteByte('K')
		}
		if g.castle&castleWQ != 0 {
			sb.WriteByte('Q')
		}
		if g.castle&castleBK != 0 {
			sb.WriteByte('k')
		}
		if g.castle&castleBQ != 0 {
			sb.WriteByte('q')
		}
	}

	// Field 4: en-passant target.
	sb.WriteByte(' ')
	if g.epSquare < 0 {
		sb.WriteByte('-')
	} else {
		sb.WriteString(squareName(g.epSquare))
	}

	// Fields 5 & 6: clocks.
	fmt.Fprintf(&sb, " %d %d", g.halfmove, g.fullmove)

	return sb.String()
}

// Turn returns "w" or "b" for the side to move.
func (g *Game) Turn() string {
	if g.turn == white {
		return "w"
	}
	return "b"
}

// clone returns a deep copy of the game (the board is a value array, so a
// struct copy suffices).
func (g *Game) clone() *Game {
	cp := *g
	return &cp
}

// findKing returns the square index of the king of the given color, or -1.
func (g *Game) findKing(c color) int {
	want := wKing
	if c == black {
		want = bKing
	}
	for s := 0; s < 64; s++ {
		if g.board[s] == want {
			return s
		}
	}
	return -1
}
