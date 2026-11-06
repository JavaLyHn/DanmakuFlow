package infra

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Unexpected SQL must fail the test even if production code swallows the SQL
// driver's error. sqlmock alone only checks that its expectations were consumed.
// Intercept at the driver level: GORM can acquire *sql.Conn directly, bypassing
// a ConnPool wrapper when running MySQL migrations.
type guardedConnector struct {
	inner       driver.Driver
	dsn         string
	t           *testing.T
	allowCreate bool
}

var mockSequence atomic.Uint64

func guardedTestDB(t *testing.T, allowCreate, monitorPings bool) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	dsn := fmt.Sprintf("guarded_%d", mockSequence.Add(1))
	inner, mock, err := sqlmock.NewWithDSN(dsn, sqlmock.MonitorPingsOption(monitorPings))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(guardedConnector{inner: inner.Driver(), dsn: dsn, t: t, allowCreate: allowCreate})
	t.Cleanup(func() {
		if db.Stats().OpenConnections > 0 {
			mock.ExpectClose()
		}
		_ = db.Close()
		mock.ExpectClose()
		_ = inner.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return db, mock
}

func (p guardedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := p.inner.Open(p.dsn)
	if err != nil {
		return nil, err
	}
	return guardedConn{Conn: conn, guard: p}, nil
}

func (p guardedConnector) Driver() driver.Driver { return p.inner }

func (p guardedConnector) check(query string) {
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

type guardedConn struct {
	driver.Conn
	guard guardedConnector
}

func (c guardedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.guard.check(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c guardedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.guard.check(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c guardedConn) Prepare(query string) (driver.Stmt, error) {
	c.guard.check(query)
	return c.Conn.Prepare(query)
}

func (c guardedConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}
