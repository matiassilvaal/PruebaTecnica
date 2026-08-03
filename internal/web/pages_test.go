package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/auth"
)

// enviarFormulario arma un POST de formulario, con cookie opcional.
func enviarFormulario(b *banco, ruta string, campos url.Values, galleta *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", ruta, strings.NewReader(campos.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if galleta != nil {
		r.AddCookie(galleta)
	}
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)
	return w
}

func cookieDeSesion(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.NombreCookie {
			return c
		}
	}
	return nil
}

func TestRaizRedirigeSegunSesion(t *testing.T) {
	b := entorno(t)

	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("sin sesión: %d → %q, quiero 302 → /login", w.Code, w.Header().Get("Location"))
	}

	_, galleta := usuarioConSesion(t, b)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(galleta)
	w = httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/player" {
		t.Errorf("con sesión: %d → %q, quiero 302 → /player", w.Code, w.Header().Get("Location"))
	}
}

func TestFormulariosSeMuestranSinSesion(t *testing.T) {
	b := entorno(t)
	for _, ruta := range []string{"/login", "/register"} {
		t.Run(ruta, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.Handler.ServeHTTP(w, httptest.NewRequest("GET", ruta, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("código = %d, quiero 200", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q", ct)
			}
			cuerpo := w.Body.String()
			if !strings.Contains(cuerpo, `method="post"`) {
				t.Error("la página no trae un formulario POST")
			}
			// La identidad de la página, no sólo que sea "una página con
			// formulario": con un único conjunto de plantillas, /login podría
			// servir el formulario de /register y `method="post"` —que ambas
			// tienen— no notaría nada.
			if !strings.Contains(cuerpo, `action="`+ruta+`"`) {
				t.Errorf("la página servida en %s no apunta a %s: ¿se está sirviendo la otra?", ruta, ruta)
			}
		})
	}
}

func TestRegistroCreaLaCuentaYDejaSesionIniciada(t *testing.T) {
	// Este test paga bcrypt con costo 12 una vez (~370 ms) a propósito: es la
	// única forma de comprobar que el handler usa auth.HashPassword de verdad.
	b := entorno(t)
	w := enviarFormulario(b, "/register", url.Values{
		"nombre":     {"Ana Prueba"},
		"email":      {"Ana@Ejemplo.CL"},
		"contrasena": {"contrasena-larga"},
	}, nil)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/player" {
		t.Fatalf("%d → %q, quiero 302 → /player", w.Code, w.Header().Get("Location"))
	}
	if cookieDeSesion(w) == nil {
		t.Fatal("no se emitió la cookie de sesión: el usuario tendría que loguearse a mano tras registrarse")
	}

	// El email quedó normalizado: si no, "ana@ejemplo.cl" y "Ana@Ejemplo.CL"
	// serían dos cuentas distintas y el login cruzado fallaría.
	u, hash, err := b.Usuarios.PorEmail(context.Background(), "ana@ejemplo.cl")
	if err != nil {
		t.Fatalf("PorEmail: %v", err)
	}
	if u.Name != "Ana Prueba" {
		t.Errorf("Name = %q", u.Name)
	}
	// La restricción más importante del bloque 03: si el handler hubiera usado
	// Store.Crear, acá estaría la contraseña en claro.
	if hash == "contrasena-larga" {
		t.Fatal("la contraseña quedó guardada en claro: el handler no pasó por cuenta.Registrar")
	}
	if !auth.VerifyPassword(hash, "contrasena-larga") {
		t.Error("el hash guardado no verifica contra la contraseña original")
	}
}

func TestRegistroInvalidoConservaLoTipeado(t *testing.T) {
	// Perder lo escrito en cada error es una molestia evitable con dos líneas.
	b := entorno(t)
	w := enviarFormulario(b, "/register", url.Values{
		"nombre":     {"Ana Prueba"},
		"email":      {"esto-no-es-un-email"},
		"contrasena": {"corta"},
	}, nil)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("código = %d, quiero 422 (y sobre todo no 500)", w.Code)
	}
	cuerpo := w.Body.String()
	if !strings.Contains(cuerpo, "esto-no-es-un-email") {
		t.Error("se perdió el email tipeado")
	}
	if !strings.Contains(cuerpo, "Ana Prueba") {
		t.Error("se perdió el nombre tipeado")
	}
	if strings.Contains(cuerpo, "corta") {
		t.Error("la contraseña se re-renderizó en el HTML: nunca debe volver al cliente")
	}
	if cookieDeSesion(w) != nil {
		t.Error("se emitió una cookie de sesión pese al error de validación")
	}
}

func TestRegistroDuplicadoNoEs500(t *testing.T) {
	b := entorno(t)
	usuarioConSesion(t, b) // ya existe ana@ejemplo.cl

	w := enviarFormulario(b, "/register", url.Values{
		"nombre":     {"Otra Ana"},
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"contrasena-larga"},
	}, nil)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("código = %d, quiero 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ana@ejemplo.cl") {
		t.Error("se perdió el email tipeado")
	}
}

func TestRegistroEscapaElHTML(t *testing.T) {
	// html/template escapa según el contexto sin que el handler haga nada;
	// este test es el que lo demuestra en vez de afirmarlo.
	b := entorno(t)
	w := enviarFormulario(b, "/register", url.Values{
		"nombre":     {`<script>alert(1)</script>`},
		"email":      {"no-sirve"},
		"contrasena": {"contrasena-larga"},
	}, nil)

	if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
		t.Fatal("el nombre salió sin escapar")
	}
}

func TestLoginCorrectoEmiteCookieYRedirige(t *testing.T) {
	b := entorno(t)
	usuarioConSesion(t, b)

	w := enviarFormulario(b, "/login", url.Values{
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"contrasena-larga"},
	}, nil)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/player" {
		t.Fatalf("%d → %q, quiero 302 → /player", w.Code, w.Header().Get("Location"))
	}
	c := cookieDeSesion(w)
	if c == nil {
		t.Fatal("no se emitió la cookie de sesión")
	}
	if !c.HttpOnly {
		t.Error("la cookie no es HttpOnly")
	}
	// El MaxAge sale del TTL de Sessions vía Guard.PonerCookie: si el handler
	// lo pusiera por su cuenta, cookie y fila caducarían en momentos distintos.
	if c.MaxAge != int(time.Hour.Seconds()) {
		t.Errorf("MaxAge = %d, quiero %d (el TTL de Sessions)", c.MaxAge, int(time.Hour.Seconds()))
	}
}

func TestLoginRotaLaSesion(t *testing.T) {
	// DestruirDeUsuario antes de Crear, contra session fixation: un token que
	// el atacante haya fijado antes del login no puede seguir sirviendo después.
	b := entorno(t)
	_, vieja := usuarioConSesion(t, b)

	w := enviarFormulario(b, "/login", url.Values{
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"contrasena-larga"},
	}, nil)
	nueva := cookieDeSesion(w)
	if nueva == nil {
		t.Fatal("no se emitió cookie")
	}
	if nueva.Value == vieja.Value {
		t.Fatal("el token no cambió: la sesión no se rotó")
	}

	// La sesión anterior tiene que haber dejado de valer.
	r := httptest.NewRequest("GET", "/player", nil)
	r.AddCookie(vieja)
	wr := httptest.NewRecorder()
	b.Handler.ServeHTTP(wr, r)
	if wr.Code != http.StatusFound {
		t.Fatalf("la sesión vieja sigue sirviendo: código = %d, quiero 302 a /login", wr.Code)
	}
}

func TestLoginMalNoDistingueSiLaCuentaExiste(t *testing.T) {
	// Se mantiene FIJO lo que el usuario tipea y se varía sólo el estado de la
	// base. Es la única comparación que aísla la propiedad de seguridad.
	//
	// Comparar dos emails DISTINTOS mediría otra cosa: el formulario devuelve
	// el email tipeado para no obligar a reescribirlo, así que dos entradas
	// distintas dan cuerpos distintos siempre — sin que eso sea una fuga. Lo
	// que el atacante observa es una respuesta a UNA entrada suya, y esa
	// respuesta no puede depender de si la cuenta existe.
	conCuenta := entorno(t)
	usuarioConSesion(t, conCuenta) // acá sí existe ana@ejemplo.cl
	sinCuenta := entorno(t)        // acá no existe ninguna cuenta

	tipeado := url.Values{
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"otra-cosa-larga"},
	}
	existe := enviarFormulario(conCuenta, "/login", tipeado, nil)
	noExiste := enviarFormulario(sinCuenta, "/login", tipeado, nil)

	for nombre, w := range map[string]*httptest.ResponseRecorder{"existe": existe, "no existe": noExiste} {
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: código = %d, quiero 401", nombre, w.Code)
		}
		if cookieDeSesion(w) != nil {
			t.Errorf("%s: se emitió cookie de sesión", nombre)
		}
	}
	if existe.Body.String() != noExiste.Body.String() {
		t.Error("los cuerpos difieren según exista la cuenta: la respuesta revela cuáles están registradas")
	}
}

func TestLoginFallidoConservaElEmailTipeado(t *testing.T) {
	// Perder el email en cada intento fallido obliga a reescribirlo, y es
	// justo donde más molesta. No es una fuga: el valor lo acaba de escribir
	// quien mira la página.
	b := entorno(t)
	usuarioConSesion(t, b)

	w := enviarFormulario(b, "/login", url.Values{
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"otra-cosa-larga"},
	}, nil)

	if !strings.Contains(w.Body.String(), "ana@ejemplo.cl") {
		t.Error("se perdió el email tipeado tras un login fallido")
	}
	if strings.Contains(w.Body.String(), "otra-cosa-larga") {
		t.Error("la contraseña volvió al HTML: nunca debe hacerlo")
	}
}

func TestLoginConEmailInexistentePagaBcrypt(t *testing.T) {
	// LA restricción del bloque 03: auth.VerificarEnVacio() no tenía llamante.
	// Sin ella, un email inexistente responde en microsegundos y uno registrado
	// paga ~370 ms: el tiempo revela qué cuentas existen aunque el mensaje sea
	// idéntico. El umbral es holgado a propósito para no volverse inestable en
	// una máquina cargada; una respuesta sin bcrypt tarda microsegundos, tres
	// órdenes de magnitud menos.
	b := entorno(t)

	inicio := time.Now()
	enviarFormulario(b, "/login", url.Values{
		"email":      {"nadie@ejemplo.cl"},
		"contrasena": {"contrasena-larga"},
	}, nil)
	transcurrido := time.Since(inicio)

	if transcurrido < 20*time.Millisecond {
		t.Fatalf("la respuesta tardó %v: el handler no llamó a auth.VerificarEnVacio(), "+
			"así que el tiempo delata qué emails están registrados", transcurrido)
	}
}

func TestLogoutSoloAceptaPost(t *testing.T) {
	// Un GET permitiría cerrarle la sesión a cualquiera con un <img src="/logout">.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/logout", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("código = %d, quiero 405", w.Code)
	}
}

func TestLogoutBorraSesionYCookie(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	w := enviarFormulario(b, "/logout", url.Values{}, galleta)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("%d → %q, quiero 302 → /login", w.Code, w.Header().Get("Location"))
	}
	if c := cookieDeSesion(w); c == nil || c.MaxAge >= 0 {
		t.Error("la cookie no se expiró en el navegador")
	}

	// Y la fila se borró de verdad: expirar sólo la cookie dejaría el token
	// vivo en la base y utilizable por quien lo hubiera copiado.
	if _, ok, err := b.Sesiones.Resolver(context.Background(), galleta.Value); err != nil || ok {
		t.Errorf("Resolver = (%v, %v): la sesión sigue viva en la base", ok, err)
	}
}

func TestPlayerExigeSesionYMuestraAlUsuario(t *testing.T) {
	b := entorno(t)

	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/player", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("sin sesión: %d → %q, quiero 302 → /login", w.Code, w.Header().Get("Location"))
	}

	u, galleta := usuarioConSesion(t, b)
	r := httptest.NewRequest("GET", "/player", nil)
	r.AddCookie(galleta)
	w = httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("con sesión: código = %d, quiero 200", w.Code)
	}
	cuerpo := w.Body.String()
	if !strings.Contains(cuerpo, u.Name) {
		t.Error("la página no saluda al usuario")
	}
	if !strings.Contains(cuerpo, "/live/stream.m3u8") {
		t.Error("la página no apunta al playlist")
	}
	// Un 200 sin directivas de caché es cacheable HEURÍSTICAMENTE por un
	// intermediario, y esta respuesta lleva el nombre del usuario adentro. El
	// razonamiento está en render(); sin esta aserción, borrar la cabecera no
	// rompería nada visible hasta que un proxy le mostrara a alguien el saludo
	// de otro.
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, quiero no-store: la página autenticada no puede cachearse", cc)
	}
}

func TestRegistroSinCamposNoEs500(t *testing.T) {
	// Un POST vacío es lo primero que prueba cualquiera con curl.
	b := entorno(t)
	w := enviarFormulario(b, "/register", url.Values{}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("código = %d, quiero 422", w.Code)
	}
}

func TestValidacionMarcaElCampoQueFallo(t *testing.T) {
	// cuenta.ErrorValidacion trae el campo justamente para poder resaltarlo.
	//
	// La aserción mira la MARCA sobre el input, no el texto del mensaje: con el
	// texto, borrar el campo Campo entero dejaría el test en verde mientras la
	// señal visual desaparecía de la página sin que nadie se enterara.
	b := entorno(t)
	w := enviarFormulario(b, "/register", url.Values{
		"nombre":     {"Ana"},
		"email":      {"ana@ejemplo.cl"},
		"contrasena": {"corta"},
	}, nil)

	cuerpo := w.Body.String()
	if !strings.Contains(cuerpo, `id="contrasena" name="contrasena" type="password" class="malo"`) {
		t.Errorf("el input de la contraseña no quedó marcado:\n%s", cuerpo)
	}
	if strings.Contains(cuerpo, `id="email" name="email" type="email" class="malo"`) {
		t.Error("se marcó el input del email, que era válido")
	}
}
