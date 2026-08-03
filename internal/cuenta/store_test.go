package cuenta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"zapping-live/internal/storage/storagetest"
)

func nuevoStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	return NewStore(storagetest.AbrirMigrada(t)), context.Background()
}

func TestCrearYRecuperar(t *testing.T) {
	s, ctx := nuevoStore(t)
	creado, err := s.Crear(ctx, "Ana Pérez", "ana@x.com", "hash-falso")
	if err != nil {
		t.Fatalf("Crear: %v", err)
	}
	if creado.ID == 0 {
		t.Error("el ID debería venir asignado")
	}
	if creado.CreatedAt.IsZero() {
		t.Error("CreatedAt debería venir poblado")
	}

	u, hash, err := s.PorEmail(ctx, "ana@x.com")
	if err != nil {
		t.Fatalf("PorEmail: %v", err)
	}
	if u.ID != creado.ID || u.Name != "Ana Pérez" || u.Email != "ana@x.com" {
		t.Errorf("PorEmail devolvió %+v", u)
	}
	if hash != "hash-falso" {
		t.Errorf("hash = %q, quiero \"hash-falso\"", hash)
	}
}

func TestCrearNormalizaElEmail(t *testing.T) {
	s, ctx := nuevoStore(t)
	if _, err := s.Crear(ctx, "Ana", "  Ana@X.COM ", "h"); err != nil {
		t.Fatalf("Crear: %v", err)
	}
	// Guardado y búsqueda deben normalizar por igual.
	if _, _, err := s.PorEmail(ctx, "ANA@x.com"); err != nil {
		t.Errorf("PorEmail con otra capitalización: %v", err)
	}
}

func TestCrearEmailDuplicado(t *testing.T) {
	s, ctx := nuevoStore(t)
	if _, err := s.Crear(ctx, "Ana", "ana@x.com", "h"); err != nil {
		t.Fatal(err)
	}
	// Distinta capitalización: debe colisionar igual.
	_, err := s.Crear(ctx, "Otra", "ANA@X.COM", "h2")
	if !errors.Is(err, ErrEmailEnUso) {
		t.Fatalf("quiero ErrEmailEnUso, tengo %v", err)
	}
}

func TestPorEmailInexistente(t *testing.T) {
	s, ctx := nuevoStore(t)
	if _, _, err := s.PorEmail(ctx, "nadie@x.com"); !errors.Is(err, ErrNoEncontrado) {
		t.Fatalf("quiero ErrNoEncontrado, tengo %v", err)
	}
}

func TestPorID(t *testing.T) {
	s, ctx := nuevoStore(t)
	creado, err := s.Crear(ctx, "Ana", "ana@x.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.PorID(ctx, creado.ID)
	if err != nil {
		t.Fatalf("PorID: %v", err)
	}
	if u.Email != "ana@x.com" {
		t.Errorf("PorID devolvió %+v", u)
	}
	if _, err := s.PorID(ctx, 99999); !errors.Is(err, ErrNoEncontrado) {
		t.Errorf("quiero ErrNoEncontrado para un ID inexistente, tengo %v", err)
	}
}

// hashDePrueba imita la firma y el prefijo de auth.HashPassword sin importar
// el paquete auth: cuenta no depende de auth, y Registrar recibe la función
// de hash inyectada justamente para mantener esa separación en los tests
// también. Deliberadamente NO incluye `plain` en el resultado, para que
// TestRegistrarValidaHasheaYPersiste pueda verificar que la contraseña en
// claro no sobrevive en la base.
func hashDePrueba(plain string) (string, error) {
	return fmt.Sprintf("$2fake$%d$largoDeLaClave", len(plain)), nil
}

func TestRegistrarRechazaDatosInvalidosSinTocarLaBase(t *testing.T) {
	s, ctx := nuevoStore(t)
	var llamado bool
	hashEspia := func(plain string) (string, error) {
		llamado = true
		return hashDePrueba(plain)
	}
	// email inválido: Validar debe rechazarlo antes de llegar a hashear o
	// insertar nada.
	_, err := Registrar(ctx, s, hashEspia, "Ana", "no-es-un-email", "unaclavelarga")
	if err == nil {
		t.Fatal("quiero error para un email inválido")
	}
	var ev ErrorValidacion
	if !errors.As(err, &ev) {
		t.Fatalf("quiero un ErrorValidacion, tengo %T: %v", err, err)
	}
	if llamado {
		t.Error("Registrar no debería llamar a hash si la validación falla")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("quedaron %d usuarios tras un alta inválida, quiero 0", n)
	}
}

func TestRegistrarValidaHasheaYPersiste(t *testing.T) {
	s, ctx := nuevoStore(t)
	u, err := Registrar(ctx, s, hashDePrueba, "Ana Pérez", "Ana@X.com", "unaclavelarga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	if u.Email != "ana@x.com" {
		t.Errorf("Email = %q, quiero \"ana@x.com\" (normalizado)", u.Email)
	}
	_, hash, err := s.PorEmail(ctx, "ana@x.com")
	if err != nil {
		t.Fatalf("PorEmail: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("password_hash = %q, quiero que empiece con \"$2\" (bcrypt)", hash)
	}
	if strings.Contains(hash, "unaclavelarga") {
		t.Error("la contraseña en claro terminó en password_hash")
	}
}

// TestCreatedAtRoundTripEntreCrearYLecturas demuestra algo que hoy es cierto
// pero que nada probaba: Crear devuelve CreatedAt truncado a segundos
// (ahora.Truncate(time.Second)) y PorEmail/PorID lo reconstruyen desde el
// entero Unix guardado (time.Unix(n, 0)); las tres vistas del mismo instante
// deben coincidir. Si alguna cambiara de representación (por ejemplo,
// guardar milisegundos, o dejar de truncar en Crear), este test lo notaría.
func TestCreatedAtRoundTripEntreCrearYLecturas(t *testing.T) {
	s, ctx := nuevoStore(t)
	creado, err := s.Crear(ctx, "Ana", "ana@x.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	porEmail, _, err := s.PorEmail(ctx, "ana@x.com")
	if err != nil {
		t.Fatalf("PorEmail: %v", err)
	}
	porID, err := s.PorID(ctx, creado.ID)
	if err != nil {
		t.Fatalf("PorID: %v", err)
	}
	if !creado.CreatedAt.Equal(porEmail.CreatedAt) {
		t.Errorf("CreatedAt de Crear = %v, de PorEmail = %v: no coinciden", creado.CreatedAt, porEmail.CreatedAt)
	}
	if !creado.CreatedAt.Equal(porID.CreatedAt) {
		t.Errorf("CreatedAt de Crear = %v, de PorID = %v: no coinciden", creado.CreatedAt, porID.CreatedAt)
	}
}

func TestUserNoExponeElHash(t *testing.T) {
	s, ctx := nuevoStore(t)
	if _, err := s.Crear(ctx, "Ana", "ana@x.com", "SECRETO"); err != nil {
		t.Fatal(err)
	}
	u, _, err := s.PorEmail(ctx, "ana@x.com")
	if err != nil {
		t.Fatal(err)
	}
	// El hash sólo viaja por el retorno explícito, nunca dentro de Usuario.
	if s := fmt.Sprintf("%+v", *u); strings.Contains(s, "SECRETO") {
		t.Errorf("el hash apareció en Usuario: %s", s)
	}
}
