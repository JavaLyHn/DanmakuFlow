package infra

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// Literal expectations define the current application contract independently
// of the production schema list. No MySQL server is contacted by these tests.
var requiredSelects = []string{
	"SELECT id, room_id, user_id, content, send_time FROM danmaku_messages LIMIT 0",
	"SELECT id, room_number FROM rooms LIMIT 0",
	"SELECT id, username FROM users LIMIT 0",
}

func schemaTestDB(t *testing.T, allowCreate bool) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	pool := guardedSQLPool{DB: sqlDB, t: t, allowCreate: allowCreate}
	db, err := connectDB(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = sqlDB.Close()
	})
	return db, mock
}

func expectRequiredSelects(mock sqlmock.Sqlmock) {
	for _, query := range requiredSelects {
		mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnRows(sqlmock.NewRows([]string{"id"})).RowsWillBeClosed()
	}
}

func TestRepeatedSchemaCheckIsReadOnly(t *testing.T) {
	db, mock := schemaTestDB(t, false)
	for i := 0; i < 2; i++ {
		expectRequiredSelects(mock)
		if err := CheckSchema(db); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemaCheckFailsWithoutRepairingMissingTableOrColumn(t *testing.T) {
	for _, failure := range []error{errors.New("table does not exist"), errors.New("unknown column"), errors.New("SELECT denied")} {
		t.Run(failure.Error(), func(t *testing.T) {
			db, mock := schemaTestDB(t, false)
			mock.ExpectQuery(regexp.QuoteMeta(requiredSelects[0])).WillReturnError(failure)
			err := CheckSchema(db)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), "cmd/migrate") {
				t.Fatalf("expected actionable schema error, got %v", err)
			}
		})
	}
}

func TestSchemaCheckReportsRowsCloseError(t *testing.T) {
	db, mock := schemaTestDB(t, false)
	failure := errors.New("read failed before result completed")
	mock.ExpectQuery(regexp.QuoteMeta(requiredSelects[0])).WillReturnRows(sqlmock.NewRows([]string{"id"}).CloseError(failure))
	if err := CheckSchema(db); !errors.Is(err, failure) {
		t.Fatalf("expected result close error, got %v", err)
	}
}

func expectCreateOnly(mock sqlmock.Sqlmock) {
	// Exact SQL properties protect data preservation, field widths and keys.
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS danmaku_messages (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			room_id VARCHAR(50) NOT NULL,
			user_id BIGINT UNSIGNED NOT NULL,
			content VARCHAR(500) NOT NULL,
			send_time DATETIME(3) NULL,
			PRIMARY KEY (id),
			KEY idx_room_time (room_id, send_time),
			KEY idx_danmaku_messages_user_id (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS rooms (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			room_number VARCHAR(20) NOT NULL,
			PRIMARY KEY (id),
			UNIQUE KEY idx_rooms_room_number (room_number)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS users (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			username VARCHAR(50) NOT NULL,
			PRIMARY KEY (id),
			UNIQUE KEY idx_users_username (username)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		mock.ExpectExec(regexp.QuoteMeta(ddl)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func TestRepeatedMigrationOnlyCreatesMissingTables(t *testing.T) {
	db, mock := schemaTestDB(t, true)
	for i := 0; i < 2; i++ {
		expectCreateOnly(mock)
		expectRequiredSelects(mock)
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationStopsOnPartialFailureAndCanBeRetried(t *testing.T) {
	db, mock := schemaTestDB(t, true)
	mock.ExpectExec("^CREATE TABLE IF NOT EXISTS danmaku_messages").WillReturnResult(sqlmock.NewResult(0, 0))
	failure := errors.New("CREATE denied")
	mock.ExpectExec("^CREATE TABLE IF NOT EXISTS rooms").WillReturnError(failure)
	if err := MigrateSchema(db); !errors.Is(err, failure) {
		t.Fatalf("expected creation error, got %v", err)
	}
	expectCreateOnly(mock)
	expectRequiredSelects(mock)
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRejectsIncompatibleExistingTablesWithoutAlteringThem(t *testing.T) {
	db, mock := schemaTestDB(t, true)
	expectCreateOnly(mock)
	failure := errors.New("unknown column room_id")
	mock.ExpectQuery(regexp.QuoteMeta(requiredSelects[0])).WillReturnError(failure)
	if err := MigrateSchema(db); !errors.Is(err, failure) {
		t.Fatalf("expected existing schema error, got %v", err)
	}
}
