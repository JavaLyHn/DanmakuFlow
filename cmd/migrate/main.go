// Command migrate creates missing tables. It never alters or resets existing ones.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/JavaLyHn/DanmakuFlow/internal/infra"
	"gorm.io/gorm"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, infra.OpenDB); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "Database initialization failed:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer, openDB func() (*gorm.DB, error)) (result error) {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: migrate\nCreates missing tables using DANMAKUFLOW_MYSQL_DSN; existing tables are preserved.")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("migrate does not accept positional arguments")
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return fmt.Errorf("access database pool: %w", err)
	}
	defer func() {
		if err := pool.Close(); result == nil && err != nil {
			result = fmt.Errorf("close database connection: %w", err)
		}
		if result == nil {
			fmt.Fprintln(output, "Database tables initialized; existing tables preserved.")
		}
	}()
	return infra.MigrateSchema(db)
}
