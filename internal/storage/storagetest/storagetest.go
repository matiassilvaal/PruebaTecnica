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
	"os"
	"path/filepath"
	"testing"
	"time"

	"zapping-live/internal/storage"
)

// Abrir crea una base en un archivo temporal que se borra al terminar el test.
//
// Deliberadamente NO usa ":memory:": con un pool de conexiones cada conexión
// abriría su propia base en memoria, y journal_mode=WAL no aplica a bases en
// memoria.
//
// Deliberadamente NO usa t.TempDir(): ese helper borra con
// os.RemoveAll y, si falla, testing.removeAll sólo reintenta
// ACCESS_DENIED/SHARING_VIOLATION. En Windows, al cerrar el último handle de
// SQLite, Defender abre el archivo un instante para escanearlo; el
// DeleteFile queda "delete-pending" y el rmdir del directorio falla con
// ERROR_DIR_NOT_EMPTY, una clase que testing.removeAll no reintenta. Por eso
// este helper es dueño de su propio directorio y lo borra con reintentos
// (ver borrarConReintentos).
func Abrir(t *testing.T) *sql.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "zapping-storagetest-")
	if err != nil {
		t.Fatalf("creando el directorio temporal: %v", err)
	}
	db, err := storage.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("abriendo base temporal: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrando la base temporal: %v", err)
		}
		borrarConReintentos(t, dir)
	})
	return db
}

// borrarConReintentos: en Windows el antivirus abre el archivo recién cerrado
// para escanearlo; el DeleteFile queda pendiente y el rmdir falla con
// ERROR_DIR_NOT_EMPTY, que testing.removeAll NO reintenta (sólo reintenta
// ACCESS_DENIED y SHARING_VIOLATION). Medido: ~0,5 % de los directorios,
// resuelto siempre en menos de 3 ms.
func borrarConReintentos(t *testing.T, dir string) {
	t.Helper()
	var ultimo error
	for espera := time.Millisecond; espera <= 512*time.Millisecond; espera *= 2 {
		if ultimo = os.RemoveAll(dir); ultimo == nil {
			return
		}
		time.Sleep(espera)
	}
	t.Logf("no se pudo borrar el temporal %s: %v", dir, ultimo)
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
