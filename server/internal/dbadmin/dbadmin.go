// Package dbadmin holds an administrator's jobs on the database that are not
// migrations: counting rows, clearing data, and looking at it.
//
// It works on local PostgreSQL and on Aurora DSQL. On DSQL it runs as the
// admin role, from an administrator's machine; the server and worker cannot
// do any of this.
package dbadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
)

// Scope is how much Clear removes.
type Scope string

const (
	// ScopeTest removes the accounts the test suites create and everything
	// those accounts own. Everyone else's data stays.
	ScopeTest Scope = "test"
	// ScopeRooms removes every room, audience session, performance, and
	// stored object, and keeps the directors and their logins.
	ScopeRooms Scope = "rooms"
	// ScopeAll removes every row and every stored object.
	ScopeAll Scope = "all"
)

// ParseScope reads a scope as typed on the command line.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case ScopeTest, ScopeRooms, ScopeAll:
		return Scope(s), nil
	}
	return "", fmt.Errorf(`scope %q is not one of "test", "rooms", "all"`, s)
}

// testUsername is exactly what the suites generate: "director-" and twelve
// hex digits (tests/e2e/fixtures/director.go), or "web-e2e-" and a number
// (web/playwright.config.ts). Nothing looser, so that a person's account is
// never taken for a test's.
var testUsername = regexp.MustCompile(`\A(director-[0-9a-f]{12}|web-e2e-[0-9]+)\z`)

// IsTestDirector reports whether a username is one a test suite made.
func IsTestDirector(username string) bool {
	return testUsername.MatchString(username)
}

// tables in the order their rows can be deleted: each is referred to only by
// tables before it.
var tables = []string{"performances", "audience_sessions", "rooms", "director_sessions", "directors"}

// TableCount is how many rows a table holds.
type TableCount struct {
	Table string
	Rows  int
}

// Counts returns the number of rows in each of the application's tables.
func Counts(ctx context.Context, pool *pgxpool.Pool) ([]TableCount, error) {
	counts := make([]TableCount, 0, len(tables))
	for _, table := range []string{"directors", "director_sessions", "rooms", "audience_sessions", "performances"} {
		var n int
		// The table name is one of the constants above, never input.
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts = append(counts, TableCount{Table: table, Rows: n})
	}
	return counts, nil
}

// Report says what Clear removed, or would remove.
type Report struct {
	Scope Scope
	// Applied is false for a dry run, in which nothing was changed.
	Applied          bool
	Directors        int
	DirectorSessions int
	Rooms            int
	AudienceSessions int
	Performances     int
	// Objects is how many stored objects were deleted. A dry run does not
	// count them and leaves this zero.
	Objects int
}

// room is one room to remove, with its performance if it has one.
type room struct {
	id            string
	performanceID string
	audience      int
}

// Clear removes the data the scope covers: the rows, and the portraits and
// recordings that belong to them. With apply false it changes nothing and
// reports what it would remove.
//
// It is safe to run again after a failure: each room is removed in one
// transaction, after its objects, so what is left is always whole rooms.
// It should not be run while a performance is in progress.
func Clear(ctx context.Context, pool *pgxpool.Pool, objects objectstore.Remover, scope Scope, apply bool) (Report, error) {
	report := Report{Scope: scope, Applied: apply}

	directors, err := directorsToRemove(ctx, pool, scope)
	if err != nil {
		return report, err
	}
	rooms, err := roomsToRemove(ctx, pool, scope, directors)
	if err != nil {
		return report, err
	}

	report.Directors = len(directors)
	report.Rooms = len(rooms)
	for _, r := range rooms {
		report.AudienceSessions += r.audience
		if r.performanceID != "" {
			report.Performances++
		}
	}
	for _, id := range directors {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM director_sessions WHERE director_id = $1`, id).Scan(&n); err != nil {
			return report, fmt.Errorf("count director sessions: %w", err)
		}
		report.DirectorSessions += n
	}
	if !apply {
		return report, nil
	}

	// Objects go before the rows that name them. If something fails in
	// between, the rows are still there to find the rest by on the next run.
	if scope == ScopeTest {
		for _, r := range rooms {
			prefixes := []string{"rooms/" + r.id + "/"}
			if r.performanceID != "" {
				prefixes = append(prefixes, "performances/"+r.performanceID+"/")
			}
			for _, prefix := range prefixes {
				n, err := objects.DeletePrefix(ctx, prefix)
				report.Objects += n
				if err != nil {
					return report, err
				}
			}
		}
	} else {
		// Every room is going, so every object is.
		for _, prefix := range []string{"rooms/", "performances/"} {
			n, err := objects.DeletePrefix(ctx, prefix)
			report.Objects += n
			if err != nil {
				return report, err
			}
		}
	}

	for _, r := range rooms {
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			for _, statement := range []string{
				`DELETE FROM performances WHERE room_id = $1`,
				`DELETE FROM audience_sessions WHERE room_id = $1`,
				`DELETE FROM rooms WHERE id = $1`,
			} {
				if _, err := tx.Exec(ctx, statement, r.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return report, fmt.Errorf("delete room %s: %w", r.id, err)
		}
	}
	for _, id := range directors {
		if err := deleteDirector(ctx, pool, id); err != nil {
			return report, fmt.Errorf("delete director %s: %w", id, err)
		}
	}
	return report, nil
}

// directorsToRemove returns the IDs of the directors the scope removes.
func directorsToRemove(ctx context.Context, pool *pgxpool.Pool, scope Scope) ([]string, error) {
	if scope == ScopeRooms {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `SELECT id::text, username FROM directors`)
	if err != nil {
		return nil, fmt.Errorf("list directors: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, username string
		if err := rows.Scan(&id, &username); err != nil {
			return nil, err
		}
		// The names are matched here and not in SQL, so the rule is the one
		// tested, on either database.
		if scope == ScopeAll || IsTestDirector(username) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// roomsToRemove returns the rooms the scope removes: those of the given
// directors, or every room.
func roomsToRemove(ctx context.Context, pool *pgxpool.Pool, scope Scope, directors []string) ([]room, error) {
	const columns = `SELECT r.id::text,
	       coalesce((SELECT p.id::text FROM performances p WHERE p.room_id = r.id), ''),
	       (SELECT count(*) FROM audience_sessions a WHERE a.room_id = r.id)
	  FROM rooms r`

	collect := func(sql string, args ...any) ([]room, error) {
		rows, err := pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("list rooms: %w", err)
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (room, error) {
			var r room
			err := row.Scan(&r.id, &r.performanceID, &r.audience)
			return r, err
		})
	}

	if scope != ScopeTest {
		return collect(columns)
	}
	var rooms []room
	for _, director := range directors {
		theirs, err := collect(columns+` WHERE r.director_id = $1`, director)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, theirs...)
	}
	return rooms, nil
}

// sessionBatch is how many login sessions one transaction deletes. Aurora
// DSQL allows a transaction to change 3,000 rows at most.
const sessionBatch = 500

// deleteDirector removes a director's login sessions, then the director.
// Their rooms must already be gone.
func deleteDirector(ctx context.Context, pool *pgxpool.Pool, id string) error {
	for {
		var deleted int64
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`DELETE FROM director_sessions
				  WHERE id IN (SELECT id FROM director_sessions WHERE director_id = $1 LIMIT $2)`,
				id, sessionBatch)
			deleted = tag.RowsAffected()
			return err
		})
		if err != nil {
			return err
		}
		if deleted < sessionBatch {
			break
		}
	}
	return db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM directors WHERE id = $1`, id)
		return err
	})
}

// readOnlyStart is how an accepted statement begins.
var readOnlyStart = regexp.MustCompile(`(?i)\A\s*(select|explain)\b`)

// explainRunning matches the forms of EXPLAIN that run the statement they
// are given, or that take options, among which that one may be.
var explainRunning = regexp.MustCompile(`(?i)\A\s*explain\s*(\(|analyze\b)`)

// CheckReadOnly refuses anything but a single SELECT or a plain EXPLAIN.
//
// It is the first of two guards on Query; the second is the read-only
// transaction the statement runs in. A SELECT can still call a function,
// and this does not look inside it.
func CheckReadOnly(sql string) error {
	if !readOnlyStart.MatchString(sql) {
		return errors.New("only a SELECT or an EXPLAIN statement can be run")
	}
	if explainRunning.MatchString(sql) {
		return errors.New("EXPLAIN with ANALYZE or options is refused: ANALYZE runs the statement")
	}
	// One statement: no semicolon except at the very end, ignoring any
	// inside a quoted string.
	body := strings.TrimRight(strings.TrimSpace(sql), ";")
	inString := false
	for _, c := range body {
		switch {
		case c == '\'':
			inString = !inString
		case c == ';' && !inString:
			return errors.New("only one statement can be run")
		}
	}
	return nil
}

// featureNotSupported is the SQLSTATE a database answers when asked for
// something it does not have.
const featureNotSupported = "0A000"

// Query runs one read-only statement and writes its rows, tab-separated,
// under a line of column names.
//
// The statement runs in a read-only transaction where the database has
// them. If it does not, the statement runs on its own, and readOnly is
// false to say that only CheckReadOnly stood guard.
func Query(ctx context.Context, pool *pgxpool.Pool, sql string, w io.Writer) (readOnly bool, err error) {
	if err := CheckReadOnly(sql); err != nil {
		return false, err
	}
	sql = strings.TrimRight(strings.TrimSpace(sql), ";")

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
		return true, writeRows(ctx, tx, sql, w)
	case errors.As(err, &pgErr) && pgErr.Code == featureNotSupported:
		return false, writeRows(ctx, pool, sql, w)
	default:
		return false, err
	}
}

// querier is what a pool and a transaction have in common.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func writeRows(ctx context.Context, q querier, sql string, w io.Writer) error {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()

	names := make([]string, len(rows.FieldDescriptions()))
	for i, field := range rows.FieldDescriptions() {
		names[i] = field.Name
	}
	fmt.Fprintln(w, strings.Join(names, "\t"))
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		cells := make([]string, len(values))
		for i, v := range values {
			cells[i] = fmt.Sprint(v)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	return rows.Err()
}
