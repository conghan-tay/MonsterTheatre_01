package migrations

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

// upStatements returns the SQL statements in a migration's Up section,
// without comments.
func upStatements(t *testing.T, content string) []string {
	t.Helper()
	_, rest, found := strings.Cut(content, "-- +goose Up")
	require.True(t, found, "the file has an Up section")
	up, _, _ := strings.Cut(rest, "-- +goose Down")

	var sql strings.Builder
	for line := range strings.SplitSeq(up, "\n") {
		if code, _, _ := strings.Cut(line, "--"); strings.TrimSpace(code) != "" {
			sql.WriteString(code + "\n")
		}
	}
	var statements []string
	for statement := range strings.SplitSeq(sql.String(), ";") {
		if s := strings.TrimSpace(statement); s != "" {
			statements = append(statements, s)
		}
	}
	return statements
}

func readAll(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	names, err := fs.Glob(fsys, "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, names)
	files := make(map[string]string, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(fsys, name)
		require.NoError(t, err)
		files[name] = string(content)
	}
	return files
}

// Aurora DSQL allows one schema statement per transaction, and goose runs a
// file's Up section as one unit.
func TestEveryMigrationHasExactlyOneUpStatement(t *testing.T) {
	for name, content := range readAll(t, FS) {
		require.Len(t, upStatements(t, content), 1, "%s", name)
	}
}

func TestForDSQLCreatesEveryIndexAsync(t *testing.T) {
	fsys, err := ForDSQL()
	require.NoError(t, err)

	original, rewritten := readAll(t, FS), readAll(t, fsys)
	require.Equal(t, len(original), len(rewritten), "the same files")

	indexes := 0
	for name, content := range rewritten {
		statement := upStatements(t, content)[0]
		was := upStatements(t, original[name])[0]
		if !strings.HasPrefix(was, "CREATE INDEX") && !strings.HasPrefix(was, "CREATE UNIQUE INDEX") {
			require.Equal(t, original[name], content, "%s is not an index and is left alone", name)
			continue
		}
		indexes++
		require.Regexp(t, `^CREATE (UNIQUE )?INDEX ASYNC \w+ ON `, statement, "%s", name)
		require.Equal(t, was, strings.Replace(statement, "INDEX ASYNC ", "INDEX ", 1), "%s: nothing else changed", name)
	}
	require.Equal(t, 2, indexes, "the two index migrations, 00008 and 00009")
}

func TestAsyncIndexes(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a plain index", "CREATE INDEX a_idx ON a (b);", "CREATE INDEX ASYNC a_idx ON a (b);"},
		{"a unique index", "CREATE UNIQUE INDEX a_idx ON a (b);", "CREATE UNIQUE INDEX ASYNC a_idx ON a (b);"},
		{"already async", "CREATE INDEX ASYNC a_idx ON a (b);", "CREATE INDEX ASYNC a_idx ON a (b);"},
		{"indented, lower case", "  create index a_idx on a (b);", "  create index ASYNC a_idx on a (b);"},
		{"a table", "CREATE TABLE a (b text);", "CREATE TABLE a (b text);"},
		{"dropping an index", "DROP INDEX a_idx;", "DROP INDEX a_idx;"},
		{"a comment that mentions one", "-- this is CREATE INDEX on DSQL\nSELECT 1;", "-- this is CREATE INDEX on DSQL\nSELECT 1;"},
	} {
		require.Equal(t, tc.want, asyncIndexes(tc.in), tc.name)
	}
}

// The application's database role is granted each table by name, so a new
// table must be added to that list or the deployed servers cannot read it.
func TestEveryTableIsGrantedToTheApplicationRole(t *testing.T) {
	createTable := regexp.MustCompile(`^CREATE TABLE (\w+)`)
	var tables []string
	for _, content := range readAll(t, FS) {
		if m := createTable.FindStringSubmatch(upStatements(t, content)[0]); m != nil {
			tables = append(tables, m[1])
		}
	}
	slices.Sort(tables)
	granted := slices.Clone(db.AppTables)
	slices.Sort(granted)
	require.Equal(t, tables, granted)
}
