// Command dbadmin does an administrator's jobs on the database that are not
// migrations.
//
// Usage:
//
//	dbadmin counts
//	dbadmin clear -scope test|rooms|all [-yes]
//	dbadmin query "SELECT ..."
//
// Like migrate, it works on the Aurora DSQL cluster named by DSQL_ENDPOINT,
// as the admin database role with the AWS identity of whoever runs it, and
// otherwise on the PostgreSQL database at DATABASE_URL. clear also deletes
// stored objects: from the bucket named by S3_BUCKET, or from
// OBJECT_STORE_DIR.
//
// clear changes nothing unless -yes is given; without it, it reports what
// it would remove.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/dbadmin"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dbadmin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("a command is needed: counts, clear, or query")
	}
	cfg := config.Load()
	// On DSQL this command is the schema owner's, whatever DSQL_USER says.
	cfg.DSQLUser = "admin"

	switch command, rest := args[0], args[1:]; command {
	case "counts":
		if len(rest) > 0 {
			return errors.New("counts takes no arguments")
		}
		pool, database, err := db.OpenConfigured(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		counts, err := dbadmin.Counts(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Printf("database: %s\n", database)
		for _, c := range counts {
			fmt.Printf("%-18s %d\n", c.Table, c.Rows)
		}
		return nil

	case "clear":
		flags := flag.NewFlagSet("clear", flag.ContinueOnError)
		scopeName := flags.String("scope", "", `what to remove: "test", "rooms", or "all"`)
		yes := flags.Bool("yes", false, "remove it; without this, only report what would be removed")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() > 0 {
			return fmt.Errorf("unexpected argument %q", flags.Arg(0))
		}
		scope, err := dbadmin.ParseScope(*scopeName)
		if err != nil {
			return err
		}
		pool, database, err := db.OpenConfigured(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		store, objectStore, err := objectstore.Configured(ctx, cfg)
		if err != nil {
			return err
		}
		remover, ok := store.(objectstore.Remover)
		if !ok {
			return fmt.Errorf("the %s object store cannot delete", objectStore)
		}

		report, err := dbadmin.Clear(ctx, pool, remover, scope, *yes)
		printReport(report, database, objectStore)
		return err

	case "query":
		if len(rest) != 1 {
			return errors.New(`query takes one argument, the statement: dbadmin query "SELECT ..."`)
		}
		pool, _, err := db.OpenConfigured(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		readOnly, err := dbadmin.Query(ctx, pool, rest[0], os.Stdout)
		if err == nil && !readOnly {
			fmt.Fprintln(os.Stderr, "note: this database has no read-only transactions; only the statement check stood guard")
		}
		return err

	default:
		return fmt.Errorf("unknown command %q (want counts, clear, or query)", command)
	}
}

func printReport(r dbadmin.Report, database, objectStore string) {
	verb := "would remove"
	if r.Applied {
		verb = "removed"
	}
	fmt.Printf("scope: %s (database: %s, object store: %s)\n", r.Scope, database, objectStore)
	for _, line := range []struct {
		what string
		n    int
	}{
		{"directors", r.Directors},
		{"director_sessions", r.DirectorSessions},
		{"rooms", r.Rooms},
		{"audience_sessions", r.AudienceSessions},
		{"performances", r.Performances},
	} {
		fmt.Printf("%s %-18s %d\n", verb, line.what, line.n)
	}
	if r.Applied {
		fmt.Printf("%s %-18s %d\n", verb, "stored objects", r.Objects)
	} else {
		fmt.Println("stored objects are not counted in a dry run; those of the rooms and performances above would go")
		fmt.Println("nothing was changed. Add -yes to remove it.")
	}
}
