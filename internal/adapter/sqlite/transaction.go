package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
)

// Inspired by https://pseudomuto.com/2018/01/clean-sql-transactions-in-golang/

// Transaction is an interface that models the standard transaction in
// database/sql.
//
// To ensure TxFn funcs cannot commit or rollback a transaction (which is
// handled by `WithTransaction`), those methods are not included here.
type Transaction interface {
	Exec(query string, args ...any) (sql.Result, error)
	ExecStmts(stmts []string) error
	Prepare(query string) (*sql.Stmt, error)
	PrepareLazy(query string) *LazyStmt
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// txWrapper wraps a native sql.Tx to fully implement the Transaction interface.
type txWrapper struct {
	*sql.Tx
}

func (tx *txWrapper) PrepareLazy(query string) *LazyStmt {
	return NewLazyStmt(tx.Tx, query)
}

func (tx *txWrapper) ExecStmts(stmts []string) error {
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// connectionTransaction is a transaction started explicitly on a pinned
// connection. SQLite does not expose BEGIN IMMEDIATE through database/sql, so
// writer transactions use this wrapper to reserve the writer lock before the
// callback performs its first query.
type connectionTransaction struct {
	conn *sql.Conn
	ctx  context.Context

	mu    sync.Mutex
	stmts []*sql.Stmt
	rows  []*sql.Rows
}

func (tx *connectionTransaction) Exec(query string, args ...any) (sql.Result, error) {
	return tx.conn.ExecContext(tx.ctx, query, args...)
}

func (tx *connectionTransaction) ExecStmts(stmts []string) error {
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (tx *connectionTransaction) Prepare(query string) (*sql.Stmt, error) {
	stmt, err := tx.conn.PrepareContext(tx.ctx, query)
	if err != nil {
		return nil, err
	}

	tx.mu.Lock()
	tx.stmts = append(tx.stmts, stmt)
	tx.mu.Unlock()
	return stmt, nil
}

func (tx *connectionTransaction) PrepareLazy(query string) *LazyStmt {
	return &LazyStmt{
		query:  query,
		ctx:    tx.ctx,
		create: func() (*sql.Stmt, error) { return tx.Prepare(query) },
		onRows: tx.trackRows,
	}
}

func (tx *connectionTransaction) Query(query string, args ...any) (*sql.Rows, error) {
	rows, err := tx.conn.QueryContext(tx.ctx, query, args...)
	if err == nil {
		tx.trackRows(rows)
	}
	return rows, err
}

func (tx *connectionTransaction) QueryRow(query string, args ...any) *sql.Row {
	return tx.conn.QueryRowContext(tx.ctx, query, args...)
}

func (tx *connectionTransaction) trackRows(rows *sql.Rows) {
	tx.mu.Lock()
	tx.rows = append(tx.rows, rows)
	tx.mu.Unlock()
}

func (tx *connectionTransaction) closeResources() error {
	tx.mu.Lock()
	rows := tx.rows
	stmts := tx.stmts
	tx.rows = nil
	tx.stmts = nil
	tx.mu.Unlock()

	var err error
	for _, rows := range rows {
		err = errors.Join(err, rows.Close())
	}
	for _, stmt := range stmts {
		err = errors.Join(err, stmt.Close())
	}
	return err
}

// TxFn is a function that will be called with an initialized Transaction
// object that can be used for executing statements and queries against a
// database.
type TxFn func(tx Transaction) error

// WithTransaction runs fn in a deferred read transaction. Callers which may
// write must use WithWriteTransaction instead.
func (db *DB) WithTransaction(fn TxFn) error {
	tx, err := db.db.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(&txWrapper{tx}); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// WithWriteTransaction runs fn after acquiring SQLite's writer reservation.
// BEGIN IMMEDIATE prevents two indexers from both reading stale state and then
// racing to upgrade their transactions on the first write.
func (db *DB) WithWriteTransaction(fn TxFn) (err error) {
	ctx := context.Background()
	conn, err := db.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close transaction connection: %w", closeErr))
		}
	}()

	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate transaction: %w", err)
	}

	txCtx, cancel := context.WithCancel(ctx)
	tx := &connectionTransaction{conn: conn, ctx: txCtx}
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			cancel()
			_ = tx.closeResources()
			if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
				discardConnection(conn)
			}
			panic(p)
		}
	}()

	err = fn(tx)
	if closeErr := tx.closeResources(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close transaction resources: %w", closeErr))
	}
	cancel()
	if err != nil {
		if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			discardConnection(conn)
			err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
		return err
	}

	if _, commitErr := conn.ExecContext(ctx, "COMMIT"); commitErr != nil {
		err = fmt.Errorf("commit transaction: %w", commitErr)
		if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			discardConnection(conn)
			err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func discardConnection(conn *sql.Conn) {
	_ = conn.Raw(func(_ any) error {
		return driver.ErrBadConn
	})
}
