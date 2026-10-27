package sqlx

import (
	"database/sql"
	"errors"
)

var (
	// ErrNotFound is an alias of sql.ErrNoRows
	ErrNotFound = sql.ErrNoRows

	// ErrTxPanic is returned when a panic occurs inside a transaction closure,
	// use errors.Is to distinguish it from business errors.
	ErrTxPanic = errors.New("panic in transaction")

	errCantNestTx    = errors.New("cannot nest transactions")
	errNoRawDBFromTx = errors.New("cannot get raw db from transaction")
)
