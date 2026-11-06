package infra

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Unexpected SQL must fail the test even if production code swallows the SQL
// driver's error. sqlmock alone only checks that its expectations were consumed.
type guardedSQLPool struct {
	*sql.DB
	t           *testing.T
	allowCreate bool
}

func (p guardedSQLPool) check(query string) {
	p.t.Helper()
	normalized := strings.ToUpper(strings.TrimSpace(query))
	if strings.HasPrefix(normalized, "SELECT ") {
		return
	}
	if p.allowCreate && strings.HasPrefix(normalized, "CREATE TABLE IF NOT EXISTS ") {
		return
	}
	p.t.Errorf("unexpected database write: %s", query)
}

func (p guardedSQLPool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	p.check(query)
	return p.DB.ExecContext(ctx, query, args...)
}

func (p guardedSQLPool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	p.check(query)
	return p.DB.QueryContext(ctx, query, args...)
}

func (p guardedSQLPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	p.check(query)
	return p.DB.QueryRowContext(ctx, query, args...)
}

func (p guardedSQLPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	p.check(query)
	return p.DB.PrepareContext(ctx, query)
}

func (p guardedSQLPool) GetDBConn() (*sql.DB, error) {
	return p.DB, nil
}
