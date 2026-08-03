// Package storage abre la base SQLite y mantiene su esquema.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite", Go puro: permite CGO_ENABLED=0
)

// pragmas se aplican a CADA conexión del pool. Importa que sea así: salvo
// journal_mode, que queda grabado en el archivo, los demás son por conexión.
//
//	busy_timeout  espera en vez de fallar con SQLITE_BUSY si hay contención
//	foreign_keys  SQLite las ignora por defecto; sin esto el CASCADE no actúa
//	journal_mode  WAL permite leer mientras se escribe
//	synchronous   NORMAL es suficiente con WAL y bastante más rápido que FULL
const pragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(1)"

// Open abre la base en `path` y verifica que responda antes de devolverla.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?"+pragmas)
	if err != nil {
		return nil, fmt.Errorf("abriendo la base %s: %w", path, err)
	}

	// SQLite es un archivo, no un servidor: el pool se mantiene chico.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// sql.Open no toca el disco. Sin este Ping, una ruta inválida no se
	// detectaría hasta el primer query, ya con el servidor arriba.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("conectando a la base %s: %w", path, err)
	}
	return db, nil
}
