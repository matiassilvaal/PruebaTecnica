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
