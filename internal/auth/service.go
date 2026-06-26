// Package auth handles identity: guest and email login, JWT access tokens, and
// rotating refresh tokens. Access tokens are short-lived and stateless; refresh
// tokens are long-lived, stored hashed, and rotated on every use so a leaked
// refresh token is usable at most once.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidToken = errors.New("invalid or expired token")

// User is the identity record returned to callers.
type User struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	IsGuest     bool   `json:"is_guest"`
	Banned      bool   `json:"banned"`
}

// Tokens is the access + refresh pair issued on login/refresh.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // access token TTL seconds
}

// Service issues and verifies tokens against the users/refresh_tokens tables.
type Service struct {
	pool       *pgxpool.Pool
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewService(pool *pgxpool.Pool, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{pool: pool, secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// GuestLogin creates a throwaway guest identity and issues tokens.
func (s *Service) GuestLogin(ctx context.Context, displayName string) (*User, *Tokens, error) {
	if displayName == "" {
		displayName = "guest"
	}
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (display_name, is_guest) VALUES ($1, true)
		 RETURNING id, display_name, is_guest, banned`,
		displayName,
	).Scan(&u.ID, &u.DisplayName, &u.IsGuest, &u.Banned)
	if err != nil {
		return nil, nil, fmt.Errorf("create guest: %w", err)
	}
	t, err := s.issue(ctx, u)
	return u, t, err
}

// EmailLogin finds-or-creates a user by email (passwordless for Phase 1; OAuth
// and password flows slot in later behind this same method) and issues tokens.
func (s *Service) EmailLogin(ctx context.Context, email, displayName string) (*User, *Tokens, error) {
	if email == "" {
		return nil, nil, errors.New("email required")
	}
	if displayName == "" {
		displayName = email
	}
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, display_name, is_guest) VALUES ($1, $2, false)
		 ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id, COALESCE(email,''), display_name, is_guest, banned`,
		email, displayName,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsGuest, &u.Banned)
	if err != nil {
		return nil, nil, fmt.Errorf("email login: %w", err)
	}
	if u.Banned {
		return nil, nil, errors.New("user is banned")
	}
	t, err := s.issue(ctx, u)
	return u, t, err
}

// Refresh validates a refresh token, rotates it (single-use), and issues a new
// pair. The old token is deleted so it cannot be replayed.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	hash := hashToken(refreshToken)
	var userID string
	var expires time.Time
	err := s.pool.QueryRow(ctx,
		`DELETE FROM refresh_tokens WHERE token_hash=$1 RETURNING user_id, expires_at`, hash,
	).Scan(&userID, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(expires) {
		return nil, ErrInvalidToken
	}
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Banned {
		return nil, errors.New("user is banned")
	}
	return s.issue(ctx, u)
}

// Logout revokes a refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash=$1`, hashToken(refreshToken))
	return err
}

// Verify parses an access token and returns the subject (user id).
func (s *Service) Verify(token string) (userID string, err error) {
	claims := jwt.RegisteredClaims{}
	t, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return s.secret, nil
	})
	if err != nil || !t.Valid {
		return "", ErrInvalidToken
	}
	return claims.Subject, nil
}

// issue mints an access JWT and a fresh stored refresh token.
func (s *Service) issue(ctx context.Context, u *User) (*Tokens, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   u.ID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
	}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return nil, err
	}

	refresh, err := randomToken()
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (token_hash, user_id, expires_at) VALUES ($1,$2,$3)`,
		hashToken(refresh), u.ID, now.Add(s.refreshTTL),
	); err != nil {
		return nil, err
	}

	return &Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(s.accessTTL.Seconds())}, nil
}

// Lookup fetches a user by id (used by the gateway to resolve display name and
// ban status during the WS handshake).
func (s *Service) Lookup(ctx context.Context, id string) (*User, error) {
	return s.userByID(ctx, id)
}

func (s *Service) userByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, COALESCE(email,''), display_name, is_guest, banned FROM users WHERE id=$1`, id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsGuest, &u.Banned)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}
