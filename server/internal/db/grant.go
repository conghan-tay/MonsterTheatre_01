package db

import (
	"errors"
	"fmt"
	"regexp"
)

// AppRole is the database role the server and worker connect as on Aurora
// DSQL. It can read and write the application's rows and nothing else: no
// schema changes, and no access to the migration tool's own table.
const AppRole = "monster_app"

// AppTables are the tables AppRole is granted, each by name. A test in the
// migrations package checks this against the tables the migrations create.
var AppTables = []string{"directors", "director_sessions", "rooms", "audience_sessions", "performances"}

// roleARN is an IAM role ARN with no path, which is what the deployment's
// task roles have.
var roleARN = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_-]{1,64}$`)

// GrantStatements returns the statements, run as admin, that let the given
// IAM roles connect as AppRole and give AppRole its tables. Each is safe to
// run again. Creating the role itself is separate, because that is not.
//
// AWS IAM GRANT takes the ARN as a literal, not a parameter, so each ARN is
// checked against the exact shape of a role ARN before it is written in.
func GrantStatements(roleARNs []string) ([]string, error) {
	if len(roleARNs) == 0 {
		return nil, errors.New("at least one IAM role ARN is needed")
	}
	var statements []string
	for _, arn := range roleARNs {
		if !roleARN.MatchString(arn) {
			return nil, fmt.Errorf("%q is not an IAM role ARN (arn:aws:iam::<account>:role/<name>)", arn)
		}
		statements = append(statements, fmt.Sprintf("AWS IAM GRANT %s TO '%s'", AppRole, arn))
	}
	// Nothing is granted on the public schema itself: DSQL refuses that,
	// and every role can already use it, though not create in it.
	for _, table := range AppTables {
		statements = append(statements, fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO %s", table, AppRole))
	}
	return statements, nil
}
