//go:build ignore

package storage

// Driver adapter for github.com/mattn/go-sqlite3 (current production driver).
// run.sh copies this file into a scratch copy of internal/storage as
// driver_spike.go with the build tag stripped.

import (
	"errors"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// sqliteDriverName is the database/sql driver name this build registers.
const sqliteDriverName = "sqlite3"

// sqliteDriverLabel identifies the driver in spike log output.
const sqliteDriverLabel = "mattn/go-sqlite3"

func isSQLiteConstraint(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint
}

// sqliteRawResultCode returns the driver's raw SQLite result code for err, or
// -1 when err is not a driver error. Used by the spike to record whether the
// driver surfaces primary or extended constraint codes.
func sqliteRawResultCode(err error) int {
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return -1
	}
	return int(sqliteErr.ExtendedCode)
}
