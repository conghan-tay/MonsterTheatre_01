package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/migrations"
)

const (
	// adminRole is the database role that owns the schema. Only this
	// command connects as it; the server and worker use db.AppRole.
	adminRole = "admin"

	indexPollInterval = 2 * time.Second
	indexWaitLimit    = 10 * time.Minute
)

// openAdmin connects to the cluster as admin through database/sql, which is
// what goose takes.
func openAdmin(ctx context.Context, endpoint string) (*sql.DB, func(), error) {
	pool, err := db.OpenDSQL(ctx, endpoint, adminRole)
	if err != nil {
		return nil, nil, err
	}
	conn := stdlib.OpenDBFromPool(pool)
	return conn, func() {
		conn.Close()
		pool.Close()
	}, nil
}

// runDSQL applies the migrations to an Aurora DSQL cluster, or reports on
// them.
//
// Two things differ from PostgreSQL (docs/decisions.md, D032). DSQL refuses
// a schema statement and a row change in one transaction, so goose is told
// to run each migration's statement apart from its own bookkeeping. And an
// index is built in the background, by CREATE INDEX ASYNC, so the files are
// read through migrations.ForDSQL and the builds are waited for afterwards.
func runDSQL(ctx context.Context, endpoint, command string) error {
	conn, closeConn, err := openAdmin(ctx, endpoint)
	if err != nil {
		return err
	}
	defer closeConn()

	files, err := migrations.ForDSQL()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectAuroraDSQL, conn, files, goose.WithIsolateDDL(true))
	if err != nil {
		return err
	}

	if command == "status" {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			fmt.Printf("%-8s %s\n", s.State, s.Source.Path)
		}
		return nil
	}

	results, err := provider.Up(ctx)
	for _, r := range results {
		fmt.Printf("applied  %s (%s)\n", r.Source.Path, r.Duration.Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("no migrations to apply")
	}
	return waitForIndexes(ctx, conn)
}

// waitForIndexes returns once no index is still being built, and fails if
// any index is left invalid, which is how DSQL leaves one whose build
// failed.
func waitForIndexes(ctx context.Context, conn *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, indexWaitLimit)
	defer cancel()

	for {
		var pending int
		err := conn.QueryRowContext(ctx,
			`SELECT count(*) FROM sys.jobs WHERE status IN ('submitted', 'processing')`).Scan(&pending)
		if err != nil {
			return fmt.Errorf("read index build jobs: %w", err)
		}
		if pending == 0 {
			break
		}
		fmt.Printf("waiting for %d index build(s)...\n", pending)
		select {
		case <-ctx.Done():
			return fmt.Errorf("index builds still running after %s", indexWaitLimit)
		case <-time.After(indexPollInterval):
		}
	}

	rows, err := conn.QueryContext(ctx,
		`SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE NOT i.indisvalid`)
	if err != nil {
		return fmt.Errorf("read index states: %w", err)
	}
	defer rows.Close()
	var invalid []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		invalid = append(invalid, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(invalid) > 0 {
		return fmt.Errorf("index build failed, leaving these invalid: %s (drop each and run up again)", strings.Join(invalid, ", "))
	}
	fmt.Println("all indexes are valid")
	return nil
}

// grant creates the application's database role if it is missing, lets the
// given IAM roles connect as it, and gives it the application's tables.
// Running it again changes nothing.
func grant(ctx context.Context, endpoint string, roleARNs []string) error {
	statements, err := db.GrantStatements(roleARNs)
	if err != nil {
		return err
	}
	conn, closeConn, err := openAdmin(ctx, endpoint)
	if err != nil {
		return err
	}
	defer closeConn()

	var exists bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, db.AppRole).Scan(&exists)
	if err != nil {
		return fmt.Errorf("look for role %s: %w", db.AppRole, err)
	}
	if !exists {
		statements = append([]string{fmt.Sprintf("CREATE ROLE %s WITH LOGIN", db.AppRole)}, statements...)
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return errors.Join(fmt.Errorf("%s", statement), err)
		}
		fmt.Println(statement)
	}
	return nil
}
