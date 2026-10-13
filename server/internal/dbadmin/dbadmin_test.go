package dbadmin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Only the exact shapes the test suites generate count as test accounts. A
// person who happened to pick a similar name must not lose their rooms.
func TestIsTestDirector(t *testing.T) {
	for _, username := range []string{
		"director-a530cd3441f8", // tests/e2e/fixtures/director.go
		"web-e2e-1760257000",    // web/playwright.config.ts
		"web-e2e-1760257000123",
	} {
		require.True(t, IsTestDirector(username), username)
	}
	for _, username := range []string{
		"conghan",
		"director-bob",
		"director-a530cd3441f",   // eleven digits
		"director-a530cd3441f8a", // thirteen
		"director-A530CD3441F8",  // the suite writes lower case
		"director-g530cd3441f8",  // not hex
		"web-e2e-",
		"web-e2e-run1",
		"my-director-a530cd3441f8",
		"director-a530cd3441f8\n",
		"",
	} {
		require.False(t, IsTestDirector(username), "%q", username)
	}
}

func TestParseScope(t *testing.T) {
	for _, s := range []string{"test", "rooms", "all"} {
		scope, err := ParseScope(s)
		require.NoError(t, err)
		require.Equal(t, Scope(s), scope)
	}
	for _, s := range []string{"", "everything", "ALL", "test "} {
		_, err := ParseScope(s)
		require.Error(t, err, "%q", s)
	}
}

func TestCheckReadOnly(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1",
		"select count(*) from rooms",
		"  SELECT id FROM rooms WHERE scene_topic = 'a; b';", // a semicolon inside a string, and one at the end
		"EXPLAIN SELECT 1",
		"explain select * from rooms",
	} {
		require.NoError(t, CheckReadOnly(sql), sql)
	}
	for _, sql := range []string{
		"",
		"   ",
		"DELETE FROM rooms",
		"UPDATE rooms SET scene_topic = ''",
		"DROP TABLE rooms",
		"INSERT INTO rooms SELECT * FROM rooms",
		"SELECT 1; DELETE FROM rooms",
		"SELECT 1; SELECT 2",
		"WITH gone AS (DELETE FROM rooms RETURNING id) SELECT * FROM gone",
		"EXPLAIN ANALYZE DELETE FROM rooms",
		"explain analyze select 1", // ANALYZE runs the statement; refused whatever it wraps
		"SELECTED",
		"-- a comment\nDELETE FROM rooms",
		"/* c */ DELETE FROM rooms",
	} {
		require.Error(t, CheckReadOnly(sql), "%q", sql)
	}
}
