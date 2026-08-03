package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"zapping-live/internal/cuenta"
	"zapping-live/internal/storage/storagetest"
)

func nuevoGuard(t *testing.T) (*Guard, *Sessions, int64, context.Context) {
	t.Helper()
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()
	us := cuenta.NewStore(db)
	u, err := us.Crear(ctx, "Ana", "ana@x.com", "h")
	if err != nil {
		t.Fatalf("creando usuario: %v", err)
	}
	sess := NewSessions(db, time.Hour)
	return NewGuard(sess, us, false), sess, u.ID, ctx
}

// eco responde 200 y deja constancia de si el usuario llegó en el contexto.
func eco(visto *bool, nombre *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UsuarioDe(r.Context()); ok {
			*visto, *nombre = true, u.Name
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequirePageSinCookieRedirige(t *testing.T) {
	g, _, _, _ := nuevoGuard(t)
	var visto bool
	var nombre string
	rec := httptest.NewRecorder()
	g.RequirePage(eco(&visto, &nombre)).ServeHTTP(rec, httptest.NewRequest("GET", "/player", nil))

	if rec.Code != http.StatusFound {
		t.Errorf("código = %d, quiero 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, quiero \"/login\"", loc)
	}
	if visto {
		t.Error("el handler protegido no debería haberse ejecutado")
	}
}

func TestRequireAPISinCookieDevuelve401(t *testing.T) {
	// Un 302 a HTML haría que hls.js intente parsear una página de login como
	// playlist y reporte un error incomprensible. Con 401 falla claro.
	g, _, _, _ := nuevoGuard(t)
	var visto bool
	var nombre string
	rec := httptest.NewRecorder()
	g.RequireAPI(eco(&visto, &nombre)).ServeHTTP(rec, httptest.NewRequest("GET", "/live/stream.m3u8", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("código = %d, quiero 401", rec.Code)
	}
	if rec.Header().Get("Location") != "" {
		t.Error("una respuesta de API no debería redirigir")
	}
	if visto {
		t.Error("el handler protegido no debería haberse ejecutado")
	}
}

func TestConSesionValidaPasaYPoneElUsuarioEnContexto(t *testing.T) {
	g, sess, uid, ctx := nuevoGuard(t)
	token, err := sess.Crear(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, caso := range []struct {
		nombre string
		mw     func(http.Handler) http.Handler
	}{
		{"page", g.RequirePage},
		{"api", g.RequireAPI},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			var visto bool
			var nombre string
			req := httptest.NewRequest("GET", "/player", nil)
			req.AddCookie(&http.Cookie{Name: NombreCookie, Value: token})
			rec := httptest.NewRecorder()
			caso.mw(eco(&visto, &nombre)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("código = %d, quiero 200", rec.Code)
			}
			if !visto {
				t.Fatal("el handler protegido debería haberse ejecutado")
			}
			if nombre != "Ana" {
				t.Errorf("usuario en contexto = %q, quiero \"Ana\"", nombre)
			}
		})
	}
}

func TestTokenInventadoNoPasa(t *testing.T) {
	g, _, _, _ := nuevoGuard(t)
	var visto bool
	var nombre string
	req := httptest.NewRequest("GET", "/player", nil)
	req.AddCookie(&http.Cookie{Name: NombreCookie, Value: "token-inventado"})
	rec := httptest.NewRecorder()
	g.RequirePage(eco(&visto, &nombre)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("código = %d, quiero 302", rec.Code)
	}
	if visto {
		t.Error("un token inventado no debería dar acceso")
	}
}

func TestSesionHuerfanaNoPasa(t *testing.T) {
	// La sesión existe pero su usuario fue borrado: el guard debe rechazar,
	// no explotar buscando un usuario inexistente.
	g, sess, uid, ctx := nuevoGuard(t)
	token, _ := sess.Crear(ctx, uid)
	if _, err := sess.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	var visto bool
	var nombre string
	req := httptest.NewRequest("GET", "/player", nil)
	req.AddCookie(&http.Cookie{Name: NombreCookie, Value: token})
	rec := httptest.NewRecorder()
	g.RequirePage(eco(&visto, &nombre)).ServeHTTP(rec, req)

	if visto {
		t.Error("una sesión sin usuario no debería dar acceso")
	}
}

func TestCookieTieneLosAtributosDeSeguridad(t *testing.T) {
	g, _, _, _ := nuevoGuard(t)
	rec := httptest.NewRecorder()
	g.PonerCookie(rec, "un-token", time.Hour)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("se pusieron %d cookies, quiero 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != NombreCookie || c.Value != "un-token" {
		t.Errorf("cookie = %s=%s", c.Name, c.Value)
	}
	if !c.HttpOnly {
		t.Error("la cookie debe ser HttpOnly: si no, un XSS puede robar la sesión")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("la cookie debe ser SameSite=Lax para mitigar CSRF")
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, quiero \"/\"", c.Path)
	}
	if c.MaxAge != int(time.Hour.Seconds()) {
		t.Errorf("MaxAge = %d, quiero %d", c.MaxAge, int(time.Hour.Seconds()))
	}
}

func TestBorrarCookieLaExpira(t *testing.T) {
	g, _, _, _ := nuevoGuard(t)
	rec := httptest.NewRecorder()
	g.BorrarCookie(rec)
	c := rec.Result().Cookies()[0]
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, quiero negativo para borrarla", c.MaxAge)
	}
}

func TestUsuarioDeSinContexto(t *testing.T) {
	if _, ok := UsuarioDe(context.Background()); ok {
		t.Error("un contexto sin usuario no debería devolver ok")
	}
}
