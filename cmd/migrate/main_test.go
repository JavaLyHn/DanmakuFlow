package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestHelpAndInvalidArgumentsNeverConnect(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--reset"}, {"unexpected"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := run(args, &output, func() (*gorm.DB, error) {
				t.Fatal("argument handling must not connect to a database")
				return nil, nil
			})
			if err == nil {
				t.Fatal("expected help or argument error")
			}
			if args[0] == "-h" && !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("expected help, got %v", err)
			}
		})
	}
}

func TestCommandReturnsConnectionError(t *testing.T) {
	failure := errors.New("database unavailable")
	err := run(nil, &bytes.Buffer{}, func() (*gorm.DB, error) { return nil, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("expected connection error, got %v", err)
	}
}

func TestCommandClosesConnectionAfterMigration(t *testing.T) {
	for _, failureStage := range []string{"none", "create", "check", "close"} {
		t.Run(failureStage, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard})
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("operation failed")
			if failureStage == "create" {
				mock.ExpectExec("^CREATE TABLE IF NOT EXISTS danmaku_messages").WillReturnError(failure)
			} else {
				for _, table := range []string{"danmaku_messages", "rooms", "users"} {
					mock.ExpectExec("^CREATE TABLE IF NOT EXISTS " + table).WillReturnResult(sqlmock.NewResult(0, 0))
				}
				if failureStage == "check" {
					mock.ExpectQuery("^SELECT .* FROM danmaku_messages LIMIT 0$").WillReturnError(failure)
				} else {
					for _, table := range []string{"danmaku_messages", "rooms", "users"} {
						mock.ExpectQuery("^SELECT .* FROM " + table + " LIMIT 0$").WillReturnRows(sqlmock.NewRows([]string{"id"})).RowsWillBeClosed()
					}
				}
			}
			close := mock.ExpectClose()
			if failureStage == "close" {
				close.WillReturnError(failure)
			}
			var output bytes.Buffer
			err = run(nil, &output, func() (*gorm.DB, error) { return db, nil })
			if failureStage == "none" && (err != nil || !strings.Contains(output.String(), "initialized")) {
				t.Fatalf("expected successful initialization: %v, %s", err, output.String())
			}
			if failureStage != "none" && !errors.Is(err, failure) {
				t.Fatalf("expected %s failure, got %v", failureStage, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
