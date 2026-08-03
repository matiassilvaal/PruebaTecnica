package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// abrirTemporal es el equivalente local de storagetest.Abrir: los tests de
// este paquete no pueden importar storagetest (sería un ciclo), así que
// duplican el helper.
//
// Deliberadamente NO usa t.TempDir(): ese helper borra con os.RemoveAll y,
// si falla, testing.removeAll sólo reintenta ACCESS_DENIED/SHARING_VIOLATION.
// En Windows, al cerrar el último handle de SQLite, Defender abre el archivo
// un instante para escanearlo; el DeleteFile queda "delete-pending" y el
// rmdir del directorio falla con ERROR_DIR_NOT_EMPTY, una clase que
// testing.removeAll no reintenta. Por eso este helper es dueño de su propio
// directorio y lo borra con reintentos.
func abrirTemporal(t *testing.T) *sql.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "zapping-storagetest-")
	if err != nil {
		t.Fatalf("creando el directorio temporal: %v", err)
	}
	db, err := Open(context.Background(), filepath.Join(dir, "test.db"))
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

func TestOpenAplicaPragmas(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()

	// foreign_keys es por conexión y SQLite lo apaga por defecto: sin él, el
	// ON DELETE CASCADE de sessions no actuaría.
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("consultando foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, quiero 1", fk)
	}

	var modo string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&modo); err != nil {
		t.Fatalf("consultando journal_mode: %v", err)
	}
	if modo != "wal" {
		t.Errorf("journal_mode = %q, quiero \"wal\"", modo)
	}
}

func TestOpenRutaInvalida(t *testing.T) {
	// Un directorio inexistente debe fallar al abrir, no al primer query.
	if _, err := Open(context.Background(), "/no/existe/este/directorio/x.db"); err == nil {
		t.Fatal("quiero error para una ruta inválida")
	}
}

func TestMigrateCreaElEsquema(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, tabla := range []string{"users", "sessions", "schema_version"} {
		var n int
		q := "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?"
		if err := db.QueryRowContext(ctx, q, tabla).Scan(&n); err != nil {
			t.Fatalf("consultando %s: %v", tabla, err)
		}
		if n != 1 {
			t.Errorf("falta la tabla %s", tabla)
		}
	}
}

func TestMigrateEsIdempotente(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	// Correr el contenedor dos veces sobre el mismo volumen no debe romper nada.
	for i := 0; i < 3; i++ {
		if err := Migrate(ctx, db); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}
	var v int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&v); err != nil {
		t.Fatalf("leyendo version: %v", err)
	}
	if v != len(migraciones) {
		t.Errorf("version = %d, quiero %d", v, len(migraciones))
	}
}

func TestMigrateNoPierdeDatos(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash, created_at) VALUES (?,?,?,?)`,
		"Ana", "ana@x.com", "hash", 1700000000)
	if err != nil {
		t.Fatalf("insertando: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate de nuevo: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("quedaron %d usuarios, quiero 1", n)
	}
}

func TestForeignKeyCascade(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash, created_at) VALUES (?,?,?,?)`,
		"Ana", "ana@x.com", "hash", 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?,?,?,?)`,
		"abc", id, 1700003600, 1700000000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("quedaron %d sesiones tras borrar el usuario, quiero 0 (CASCADE)", n)
	}
}

// TestSchemaVersionInicializacionConcurrente reproduce, de forma
// determinista, la carrera que dos conexiones distintas pueden pisar al
// arrancar el contenedor dos veces sobre el mismo archivo: ambas ven la
// tabla vacía antes de que cualquiera inserte. Usa dos *sql.Conn dedicadas
// (en vez de goroutines) para forzar ese entrelazado sin depender del
// scheduler.
//
// Con la restricción CHECK(id=1) de crearSchemaVersion, el segundo
// INSERT OR IGNORE no hace nada y la tabla queda con 1 fila. Sin esa
// restricción (la tabla vieja sólo tenía `version INTEGER NOT NULL`, sin
// clave primaria), las dos inserciones tendrían éxito y quedarían 2 filas:
// este test falla si esa restricción desaparece.
func TestSchemaVersionInicializacionConcurrente(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, crearSchemaVersion); err != nil {
		t.Fatalf("creando schema_version: %v", err)
	}

	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("abriendo conn1: %v", err)
	}
	defer conn1.Close()
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("abriendo conn2: %v", err)
	}
	defer conn2.Close()

	// Ambas conexiones consultan antes de que cualquiera inserte: las dos
	// ven la tabla vacía, como en la reproducción del revisor.
	for i, c := range []*sql.Conn{conn1, conn2} {
		var v int
		err := c.QueryRowContext(ctx, leerSchemaVersion).Scan(&v)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("conexión %d: quería sql.ErrNoRows antes de inicializar, obtuve %v", i+1, err)
		}
	}

	// Ambas intentan inicializar, en el orden en que lo haría Migrate.
	if _, err := conn1.ExecContext(ctx, inicializarSchemaVersion); err != nil {
		t.Fatalf("inicializando desde conn1: %v", err)
	}
	if _, err := conn2.ExecContext(ctx, inicializarSchemaVersion); err != nil {
		t.Fatalf("inicializando desde conn2: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_version").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_version quedó con %d filas tras la carrera, quiero 1", n)
	}
}

// TestMigrateSchemaVersionUnaSolaFila confirma que, tras varias corridas de
// Migrate, schema_version sigue teniendo exactamente una fila con la
// versión correcta: el UPDATE sin WHERE del bug original mantenía las filas
// sincronizadas (sin pérdida de datos), pero dejaba basura silenciosa.
func TestMigrateSchemaVersionUnaSolaFila(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := Migrate(ctx, db); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_version").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_version tiene %d filas, quiero 1", n)
	}
	var v int
	if err := db.QueryRowContext(ctx, leerSchemaVersion).Scan(&v); err != nil {
		t.Fatalf("leyendo version: %v", err)
	}
	if v != len(migraciones) {
		t.Errorf("version = %d, quiero %d", v, len(migraciones))
	}
}

// TestSchemaVersionFormaNueva verifica que una base recién creada adopte la
// forma nueva de schema_version (columna id con CHECK). CREATE TABLE IF NOT
// EXISTS no migraría una base ya creada con la forma vieja, pero no hay
// bases en producción todavía, así que no hace falta resolverlo aquí.
func TestSchemaVersionFormaNueva(t *testing.T) {
	db := abrirTemporal(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(schema_version)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	var tieneID bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("leyendo columna: %v", err)
		}
		if name == "id" && pk == 1 {
			tieneID = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !tieneID {
		t.Error("schema_version no tiene una columna id como clave primaria")
	}
}
