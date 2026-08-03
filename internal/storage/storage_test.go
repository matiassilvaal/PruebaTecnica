package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func abrirTemporal(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("abriendo base temporal: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
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
