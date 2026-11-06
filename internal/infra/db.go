package infra

import (
	"fmt"
	"os"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const databaseDSNEnv = "DANMAKUFLOW_MYSQL_DSN"

// OpenDB connects to the explicitly configured database. It never changes its
// schema or data. Call CheckSchema before starting a runtime service.
func OpenDB() (*gorm.DB, error) {
	cfg, err := databaseConfig()
	if err != nil {
		return nil, err
	}
	return connectDB(mysql.New(mysql.Config{DSNConfig: cfg}))
}

func databaseConfig() (*mysqldriver.Config, error) {
	dsn := os.Getenv(databaseDSNEnv)
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%s is required", databaseDSNEnv)
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		// Parser errors may include pieces of the DSN. Never echo credentials.
		return nil, fmt.Errorf("%s is not a valid MySQL DSN", databaseDSNEnv)
	}
	if strings.TrimSpace(cfg.DBName) == "" {
		return nil, fmt.Errorf("%s must name a database", databaseDSNEnv)
	}
	if cfg.Timeout < 0 || cfg.ReadTimeout < 0 || cfg.WriteTimeout < 0 {
		return nil, fmt.Errorf("%s timeouts must not be negative", databaseDSNEnv)
	}
	cfg.ParseTime = true // DanmakuMessage.SendTime is a time.Time.
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 5 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	return cfg, nil
}

func connectDB(dialector gorm.Dialector) (*gorm.DB, error) {
	// Callers log returned errors. Avoid a second ORM log during initialization.
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		if db != nil {
			if pool, poolErr := db.DB(); poolErr == nil {
				_ = pool.Close()
			}
		}
		return nil, fmt.Errorf("connect MySQL: %w", err)
	}
	return db, nil
}
