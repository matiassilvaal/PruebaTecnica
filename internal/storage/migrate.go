package storage

import (
	"context"
	"database/sql"
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

// crearSchemaVersion fija la fila de versión a id=1 mediante una restricción
// CHECK en la clave primaria: la base misma rechaza una segunda fila, en vez
// de depender de que el código coordine el SELECT y el INSERT que la crean.
//
// Dos conexiones que corren Migrate al mismo tiempo pueden ambas ver
// ErrNoRows antes de que cualquiera inserte (SELECT e INSERT no van en la
// misma transacción). Con esta restricción eso no importa: el segundo
// INSERT OR IGNORE choca contra la clave primaria y no hace nada.
const crearSchemaVersion = `CREATE TABLE IF NOT EXISTS schema_version (
	id      INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
)`

// inicializarSchemaVersion es idempotente por construcción gracias al
// CHECK(id=1) de crearSchemaVersion: si otra conexión ya insertó la fila,
// este no hace nada, sin necesidad de coordinarse con ella.
const inicializarSchemaVersion = `INSERT OR IGNORE INTO schema_version (id, version) VALUES (1, 0)`

const leerSchemaVersion = `SELECT version FROM schema_version WHERE id = 1`

// Migrate lleva el esquema a la última versión. Es idempotente: correr el
// contenedor dos veces sobre el mismo volumen no rompe nada ni pierde datos.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, crearSchemaVersion); err != nil {
		return fmt.Errorf("creando schema_version: %w", err)
	}

	if _, err := db.ExecContext(ctx, inicializarSchemaVersion); err != nil {
		return fmt.Errorf("inicializando schema_version: %w", err)
	}

	var version int
	err := db.QueryRowContext(ctx, leerSchemaVersion).Scan(&version)
	if err != nil {
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
		`UPDATE schema_version SET version = ? WHERE id = 1`, len(migraciones)); err != nil {
		return fmt.Errorf("actualizando schema_version: %w", err)
	}
	return tx.Commit()
}
