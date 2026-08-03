package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// migraciones se aplican en orden. Para agregar uno nuevo, APÉNDALO al final:
// nunca edites ni reordenes los existentes, porque la versión guardada en la
// base es un índice sobre este slice.
var migraciones = []string{
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		name          TEXT    NOT NULL,
		email         TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		created_at    INTEGER NOT NULL
	)`,
	`CREATE TABLE sessions (
		token_hash TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX idx_sessions_expires ON sessions(expires_at)`,
}

// Migrate lleva el esquema a la última versión. Es idempotente: correr el
// contenedor dos veces sobre el mismo volumen no rompe nada ni pierde datos.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creando schema_version: %w", err)
	}

	var version int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("inicializando schema_version: %w", err)
		}
		version = 0
	case err != nil:
		return fmt.Errorf("leyendo schema_version: %w", err)
	}

	if version >= len(migraciones) {
		return nil
	}

	// Todo o nada: si una migración falla, la base queda como estaba.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("abriendo transacción de migración: %w", err)
	}
	defer tx.Rollback()

	for i, m := range migraciones[version:] {
		if _, err := tx.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("aplicando migración %d: %w", version+i, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE schema_version SET version = ?`, len(migraciones)); err != nil {
		return fmt.Errorf("actualizando schema_version: %w", err)
	}
	return tx.Commit()
}
