package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/cuenta"
	"zapping-live/internal/storage/storagetest"
)

// TestFlujoCompletoDeAutenticacion recorre la cadena entera que ningún otro
// test ejercita de punta a punta:
//
//	Validar (dentro de Registrar) → HashPassword → Store.Crear
//	  → PorEmail + VerifyPassword → Sessions.Crear → RequirePage
//
// Cada pieza está probada por separado, pero nada garantizaba que encajaran:
// es justo el seam donde vivían "Crear sin validar" (Registrar lo cierra) y
// "el login filtra tiempos" (VerificarEnVacio lo cierra, aunque acá no se
// ejercita: esta prueba cubre el camino feliz y el de contraseña incorrecta,
// no la rama de email inexistente).
func TestFlujoCompletoDeAutenticacion(t *testing.T) {
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()
	store := cuenta.NewStore(db)
	sess := NewSessions(db, time.Hour)
	guard := NewGuard(sess, store, false)

	protegido := guard.RequirePage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	pedirConCookie := func(token string) int {
		req := httptest.NewRequest("GET", "/player", nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: NombreCookie, Value: token})
		}
		rec := httptest.NewRecorder()
		protegido.ServeHTTP(rec, req)
		return rec.Code
	}

	// Registro: Registrar valida, hashea con bcrypt de verdad (no un fake) y
	// persiste en un solo paso.
	if _, err := cuenta.Registrar(ctx, store, HashPassword, "Ana Pérez", "ana@x.com", "unaclavelarga"); err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	usuario, hash, err := store.PorEmail(ctx, "ana@x.com")
	if err != nil {
		t.Fatalf("PorEmail: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password_hash = %q, quiero que empiece con \"$2\" (bcrypt)", hash)
	}

	// Contraseña errónea: VerifyPassword rechaza; sin sesión, RequirePage
	// nunca deja pasar (no hay token que probar: el login no llegó a crear
	// ninguna sesión).
	if VerifyPassword(hash, "otraclave") {
		t.Fatal("VerifyPassword no debería aceptar una contraseña incorrecta")
	}

	// Login correcto: VerifyPassword acepta, Sessions.Crear emite el token,
	// y la cookie resultante pasa RequirePage.
	if !VerifyPassword(hash, "unaclavelarga") {
		t.Fatal("VerifyPassword debería aceptar la contraseña correcta")
	}
	token, err := sess.Crear(ctx, usuario.ID)
	if err != nil {
		t.Fatalf("Sessions.Crear: %v", err)
	}
	if code := pedirConCookie(token); code != http.StatusOK {
		t.Errorf("con sesión recién creada, código = %d, quiero 200", code)
	}

	// Logout: tras Destruir, la misma cookie deja de servir.
	if err := sess.Destruir(ctx, token); err != nil {
		t.Fatalf("Destruir: %v", err)
	}
	if code := pedirConCookie(token); code != http.StatusFound {
		t.Errorf("tras logout, código = %d, quiero 302 (RequirePage sin sesión)", code)
	}
}
