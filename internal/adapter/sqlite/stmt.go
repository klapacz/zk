package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// LazyStmt is a wrapper around a sql.Stmt which will be evaluated on first use.
type LazyStmt struct {
	query  string
	create func() (*sql.Stmt, error)
	ctx    context.Context
	onRows func(*sql.Rows)
	stmt   *sql.Stmt
	err    error
	once   sync.Once
}

// NewLazyStmt creates a new lazy statement bound to the given transaction.
func NewLazyStmt(tx *sql.Tx, query string) *LazyStmt {
	return &LazyStmt{
		query:  query,
		create: func() (*sql.Stmt, error) { return tx.Prepare(query) },
	}
}

func (s *LazyStmt) Stmt() (*sql.Stmt, error) {
	s.once.Do(func() {
		s.stmt, s.err = s.create()
	})
	return s.stmt, s.wrapErr(s.err)
}

func (s *LazyStmt) Exec(args ...any) (sql.Result, error) {
	stmt, err := s.Stmt()
	if err != nil {
		return nil, err
	}
	var res sql.Result
	if s.ctx == nil {
		res, err = stmt.Exec(args...)
	} else {
		res, err = stmt.ExecContext(s.ctx, args...)
	}
	return res, s.wrapErr(err)
}

func (s *LazyStmt) Query(args ...any) (*sql.Rows, error) {
	stmt, err := s.Stmt()
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if s.ctx == nil {
		rows, err = stmt.Query(args...)
	} else {
		rows, err = stmt.QueryContext(s.ctx, args...)
	}
	if err == nil && s.onRows != nil {
		s.onRows(rows)
	}
	return rows, s.wrapErr(err)
}

func (s *LazyStmt) QueryRow(args ...any) (*sql.Row, error) {
	stmt, err := s.Stmt()
	if err != nil {
		return nil, err
	}
	if s.ctx == nil {
		return stmt.QueryRow(args...), nil
	}
	return stmt.QueryRowContext(s.ctx, args...), nil
}

func (s *LazyStmt) wrapErr(err error) error {
	if err != nil {
		return fmt.Errorf("database query: %s: %w", s.query, err)
	}
	return nil
}
