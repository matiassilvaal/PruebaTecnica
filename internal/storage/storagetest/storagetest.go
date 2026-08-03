// Package storagetest arma bases de prueba para los tests de otros paquetes.
//
// Vive en un subpaquete y no en `storage` porque un archivo no-_test.go que
// importe `testing` arrastraría ese paquete al binario de producción y
// registraría sus flags. Los tests del propio `storage` no pueden importarlo
// (sería un ciclo) y definen su helper local.
package storagetest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"zapping-live/internal/storage"
)

// Abrir crea una base en un archivo temporal que se borra al terminar el test.
//
// Deliberadamente NO usa ":memory:": con un pool de conexiones cada conexión
// abriría su propia base en memoria, y journal_mode=WAL no aplica a bases en
// memoria.
func Abrir(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("abriendo base temporal: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// AbrirMigrada es Abrir con el esquema ya aplicado.
func AbrirMigrada(t *testing.T) *sql.DB {
	t.Helper()
	db := Abrir(t)
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrando base temporal: %v", err)
	}
	return db
}
