// Package migrations embeds the SQL schema and applies it on startup, so Phoenix
// boots against any empty Postgres (local Docker, Neon, Supabase, RDS) with zero
// manual steps. All statements are idempotent (CREATE ... IF NOT EXISTS), so
// running them every boot is safe.
package migrations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var FS embed.FS

// Apply runs every embedded .sql file in lexical order, statement by statement.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := FS.ReadFile(name)
		if err != nil {
			return err
		}
		// Strip -- comments from the whole file FIRST, so a ';' inside a comment
		// can't split a statement, then split on statement boundaries.
		clean := stripComments(string(data))
		for _, raw := range strings.Split(clean, ";") {
			stmt := strings.TrimSpace(raw)
			if stmt == "" {
				continue
			}
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// stripComments removes blank and -- comment lines so comment-only fragments
// (e.g. trailing text after the last ;) don't become empty queries.
func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
