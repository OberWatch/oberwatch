//go:build ignore

package storage

// Driver adapter for modernc.org/sqlite (pure Go, transpiled SQLite).
// run.sh copies this file into a scratch copy of internal/storage as
// driver_spike.go with the build tag stripped.

import (
	"errors"

	sqlite "modernc.org/sqlite"
)

// sqliteDriverName is the database/sql driver name this build registers.
// modernc registers "sqlite", not "sqlite3".
const sqliteDriverName = "sqlite"

// sqliteDriverLabel identifies the driver in spike log output.
const sqliteDriverLabel = "modernc.org/sqlite"

// sqliteConstraint is SQLITE_CONSTRAINT. modernc reports the raw result code,
// which for a UNIQUE violation is the extended code
// SQLITE_CONSTRAINT_UNIQUE (19 | 8<<8 = 2067), so the low byte has to be
// masked off before comparing.
const sqliteConstraint = 19

func isSQLiteConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteConstraint
}

// sqliteRawResultCode returns the driver's raw SQLite result code for err, or
// -1 when err is not a driver error. Used by the spike to record whether the
// driver surfaces primary or extended constraint codes.
func sqliteRawResultCode(err error) int {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return -1
	}
	return sqliteErr.Code()
}
