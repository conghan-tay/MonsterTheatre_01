// Command migrate applies the embedded SQL migrations.
//
// Usage: migrate [up|down|status]   (default: up)
//
//	migrate grant ROLE_ARN...      (Aurora DSQL only)
//
// With DSQL_ENDPOINT set it works on that Aurora DSQL cluster, as the admin
// database role, with the AWS identity of whoever runs it. Otherwise it works
// on the PostgreSQL database at DATABASE_URL.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/migrations"
)

func main() {
	command := "up"
	var args []string
	if len(os.Args) > 1 {
		command, args = os.Args[1], os.Args[2:]
	}
	if err := run(context.Background(), command, args); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, command string, args []string) error {
	cfg := config.Load()
	onDSQL := cfg.DSQLEndpoint != ""

	switch command {
	case "up", "status":
	case "down":
		// One Down section drops a column, which DSQL does not support, and
		// nothing deployed is rolled back by hand.
		if onDSQL {
			return fmt.Errorf("down is not supported on Aurora DSQL")
		}
	case "grant":
		if !onDSQL {
			return fmt.Errorf("grant is for Aurora DSQL; set DSQL_ENDPOINT")
		}
		return grant(ctx, cfg.DSQLEndpoint, args)
	default:
		return fmt.Errorf("unknown command %q (want up, down, status, or grant)", command)
	}
	if len(args) > 0 {
		return fmt.Errorf("%s takes no arguments", command)
	}

	if onDSQL {
		return runDSQL(ctx, cfg.DSQLEndpoint, command)
	}

	conn, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.RunContext(ctx, command, conn, ".")
}
