package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrantStatements(t *testing.T) {
	statements, err := GrantStatements([]string{
		"arn:aws:iam::123456789012:role/MonsterTheatreData-ServerTaskRole1A2B3C-xYz012",
		"arn:aws:iam::123456789012:role/worker_task.role+1=a,b@c",
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		`AWS IAM GRANT monster_app TO 'arn:aws:iam::123456789012:role/MonsterTheatreData-ServerTaskRole1A2B3C-xYz012'`,
		`AWS IAM GRANT monster_app TO 'arn:aws:iam::123456789012:role/worker_task.role+1=a,b@c'`,
		`GRANT USAGE ON SCHEMA public TO monster_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON directors TO monster_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON director_sessions TO monster_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON rooms TO monster_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON audience_sessions TO monster_app`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON performances TO monster_app`,
	}, statements)
}

// The ARN is written into the statement, which takes no parameter, so
// anything that is not exactly a role ARN is refused.
func TestGrantStatementsRefusesWhatIsNotARoleARN(t *testing.T) {
	for _, arn := range []string{
		"",
		"monster_app",
		"arn:aws:iam::123456789012:user/alice",
		"arn:aws:iam::123456789012:root",
		"arn:aws:iam::12345:role/short-account",
		"arn:aws:iam::123456789012:role/",
		"arn:aws:iam::123456789012:role/with/path",
		"arn:aws:iam::123456789012:role/x'; DROP TABLE rooms; --",
		"arn:aws:iam::123456789012:role/has space",
		" arn:aws:iam::123456789012:role/leading-space",
		"arn:aws:iam::123456789012:role/trailing-newline\n",
	} {
		_, err := GrantStatements([]string{"arn:aws:iam::123456789012:role/fine", arn})
		require.Error(t, err, "%q", arn)
	}

	_, err := GrantStatements(nil)
	require.Error(t, err, "at least one role is needed")
}
