//go:build ignore

package storage

// Driver adapter for github.com/ncruces/go-sqlite3 (pure Go, wazero/WASM).
// run.sh copies this file into a scratch copy of internal/storage as
// driver_spike.go with the build tag stripped.

import (
	"errors"

	"github.com/ncruces/go-sqlite3"
	// Register the database/sql driver.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// sqliteDriverName is the database/sql driver name this build registers.
const sqliteDriverName = "sqlite3"

// sqliteDriverLabel identifies the driver in spike log output.
const sqliteDriverLabel = "ncruces/go-sqlite3"

func isSQLiteConstraint(err error) bool {
	// ncruces' ErrorCode is a uint8, so Error.Code() already truncates an
	// extended code down to its primary code.
	return errors.Is(err, sqlite3.CONSTRAINT)
}

// sqliteRawResultCode returns the driver's raw SQLite result code for err, or
// -1 when err is not a driver error. Used by the spike to record whether the
// driver surfaces primary or extended constraint codes.
func sqliteRawResultCode(err error) int {
	var sqliteErr *sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return -1
	}
	return int(sqliteErr.ExtendedCode())
}
