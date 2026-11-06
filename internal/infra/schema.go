package infra

import (
	"fmt"

	"gorm.io/gorm"
)

// These initial definitions are deliberately independent of AutoMigrate:
// changing a Go model must not implicitly alter an existing database.
var initialTables = []struct {
	name   string
	create string
	check  string
}{
	{
		name: "danmaku_messages",
		create: `CREATE TABLE IF NOT EXISTS danmaku_messages (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			room_id VARCHAR(50) NOT NULL,
			user_id BIGINT UNSIGNED NOT NULL,
			content VARCHAR(500) NOT NULL,
			send_time DATETIME(3) NOT NULL,
			PRIMARY KEY (id),
			KEY idx_room_time (room_id, send_time),
			KEY idx_danmaku_messages_user_id (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		check: "SELECT id, room_id, user_id, content, send_time FROM danmaku_messages LIMIT 0",
	},
	{
		name: "rooms",
		create: `CREATE TABLE IF NOT EXISTS rooms (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			room_number VARCHAR(20) NOT NULL,
			PRIMARY KEY (id),
			UNIQUE KEY idx_rooms_room_number (room_number)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		check: "SELECT id, room_number FROM rooms LIMIT 0",
	},
	{
		name: "users",
		create: `CREATE TABLE IF NOT EXISTS users (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
			username VARCHAR(50) NOT NULL,
			PRIMARY KEY (id),
			UNIQUE KEY idx_users_username (username)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		check: "SELECT id, username FROM users LIMIT 0",
	},
}

// CheckSchema only checks that required tables/columns are readable. It does
// not read records, repair tables or verify every type/index constraint.
func CheckSchema(db *gorm.DB) error {
	for _, table := range initialTables {
		rows, err := db.Raw(table.check).Rows()
		if err == nil {
			err = rows.Close()
			if err == nil {
				err = rows.Err()
			}
		}
		if err != nil {
			return fmt.Errorf("check table %s: %w; run go run ./cmd/migrate for missing tables; review existing schema or permissions separately", table.name, err)
		}
	}
	return nil
}

// MigrateSchema initializes missing tables only. MySQL DDL is not an atomic
// three-table transaction; IF NOT EXISTS makes a partial run safe to retry.
// Never call this from a runtime service's startup path.
func MigrateSchema(db *gorm.DB) error {
	for _, table := range initialTables {
		if err := db.Exec(table.create).Error; err != nil {
			return fmt.Errorf("initialize table %s: %w", table.name, err)
		}
	}
	return CheckSchema(db)
}
