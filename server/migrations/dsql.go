package migrations

import (
	"io/fs"
	"regexp"
	"testing/fstest"
)

// createIndex matches the start of a statement that creates an index, with
// or without ASYNC. A comment line cannot match: it starts with "--".
var createIndex = regexp.MustCompile(`(?im)^([ \t]*CREATE[ \t]+(?:UNIQUE[ \t]+)?INDEX)[ \t]+(?:ASYNC[ \t]+)?`)

// asyncIndexes rewrites every CREATE INDEX statement as CREATE INDEX ASYNC.
func asyncIndexes(sql string) string {
	return createIndex.ReplaceAllString(sql, "${1} ASYNC ")
}

// ForDSQL returns the migrations as Aurora DSQL accepts them. The files are
// the same ones, with one difference: DSQL builds every index in the
// background and refuses CREATE INDEX without ASYNC, which PostgreSQL in
// turn does not know (docs/decisions.md, D032).
func ForDSQL() (fs.FS, error) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return nil, err
	}
	// MapFS is an in-memory file system from the standard library. It lives
	// in a testing package, but is an ordinary fs.FS.
	rewritten := make(fstest.MapFS, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(FS, name)
		if err != nil {
			return nil, err
		}
		rewritten[name] = &fstest.MapFile{Data: []byte(asyncIndexes(string(content)))}
	}
	return rewritten, nil
}
