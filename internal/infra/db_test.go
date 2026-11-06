package infra

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
)

func TestOpenDBRejectsMissingOrInvalidConfiguration(t *testing.T) {
	for _, dsn := range []string{"", "  ", "not-a-dsn", "app:secret@tcp(localhost:3306)/", "app:secret@tcp(localhost:3306)/db?timeout=secret"} {
		t.Run(dsn, func(t *testing.T) {
			t.Setenv("DANMAKUFLOW_MYSQL_DSN", dsn)
			db, err := OpenDB()
			if err == nil || db != nil {
				t.Fatal("invalid configuration must fail before connecting")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("configuration error disclosed the password or raw DSN")
			}
		})
	}
}

func TestDatabaseConfigurationPreservesExplicitTarget(t *testing.T) {
	t.Setenv("DANMAKUFLOW_MYSQL_DSN", "app:p@ss@tcp(db.example:3307)/retained_db?parseTime=false&loc=UTC&timeout=9s")
	cfg, err := databaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "app" || cfg.Passwd != "p@ss" || cfg.Addr != "db.example:3307" || cfg.DBName != "retained_db" {
		t.Fatal("explicit connection target or credentials changed")
	}
	if !cfg.ParseTime || cfg.Timeout != 9*time.Second || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 {
		t.Fatal("datetime parsing and bounded IO are required; explicit timeout must be preserved")
	}
}

func TestDatabaseConfigurationRejectsNegativeTimeouts(t *testing.T) {
	for _, option := range []string{"timeout", "readTimeout", "writeTimeout"} {
		t.Run(option, func(t *testing.T) {
			t.Setenv("DANMAKUFLOW_MYSQL_DSN", "app:secret@tcp(localhost:3306)/db?"+option+"=-1s")
			if _, err := databaseConfig(); err == nil {
				t.Fatal("negative timeouts must be rejected before a connection is attempted")
			}
		})
	}
}

func TestRepeatedConnectionExecutesNoSchemaOrDataWrites(t *testing.T) {
	// Only version detection and Ping are permitted. Any DROP, ALTER,
	// CREATE, TRUNCATE or DML issued by the real GORM path fails this test.
	for i := 0; i < 2; i++ {
		sqlDB, mock := guardedTestDB(t, false, true)
		mock.ExpectQuery("SELECT VERSION\\(\\)").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.36"))
		mock.ExpectPing()
		mock.ExpectClose()
		db, err := connectDB(mysql.New(mysql.Config{Conn: sqlDB}))
		if err != nil {
			t.Fatalf("read-only connection failed: %v", err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Close(); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectionFailureClosesOwnedPool(t *testing.T) {
	for _, stage := range []string{"version", "ping"} {
		t.Run(stage, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("database unavailable")
			version := mock.ExpectQuery("SELECT VERSION\\(\\)")
			if stage == "version" {
				version.WillReturnError(failure)
			} else {
				version.WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.36"))
				mock.ExpectPing().WillReturnError(failure)
			}
			mock.ExpectClose()
			db, err := connectDB(mysql.New(mysql.Config{Conn: sqlDB}))
			if db != nil || !errors.Is(err, failure) {
				t.Fatalf("expected connection failure, got db=%v error=%v", db, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
