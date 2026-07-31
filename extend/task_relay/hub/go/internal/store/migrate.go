package store

import "database/sql"

func applySQLiteMigrations(db *sql.DB) {
	for _, stmt := range sqliteMigrations {
		_, _ = db.Exec(stmt)
	}
}

func applyPostgresMigrations(db *sql.DB) {
	for _, stmt := range postgresMigrations {
		_, _ = db.Exec(stmt)
	}
}
