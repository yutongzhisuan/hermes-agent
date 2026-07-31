package store

import "database/sql"

func applySQLiteMigrations(db *sql.DB) {
	for _, stmt := range sqliteMigrations {
		_, _ = db.Exec(stmt)
	}
}
