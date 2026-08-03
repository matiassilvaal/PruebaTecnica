package auth

import (
	"context"
	"log"
	"net/http"

	"zapping-live/internal/cuenta"
)

const NombreCookie = "zapping_session"

type claveContexto struct{}

var claveUsuario claveContexto

// Guard protege rutas exigiendo una sesión válida.
//
// Expone DOS middlewares en vez de uno que adivine: el router sabe qué es una
// página y qué es una llamada de datos, así que la decisión se toma ahí y no
// inspeccionando cabeceras que el cliente controla.
type Guard struct {
	sesiones       *Sessions
	usuarios       *cuenta.Store
	cookiesSeguras bool
}

func NewGuard(s *Sessions, u *cuenta.Store, cookiesSeguras bool) *Guard {
	return &Guard{sesiones: s, usuarios: u, cookiesSeguras: cookiesSeguras}
}

// RequirePage protege páginas HTML: sin sesión, manda al login.
func (g *Guard) RequirePage(next http.Handler) http.Handler {
	return g.proteger(next, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// RequireAPI protege el playlist, los segmentos y el SSE: sin sesión, 401.
//
// Un 302 acá sería peor que inútil: hls.js intentaría parsear la página de
// login como playlist y reportaría un error incomprensible. Con 401 el player
// falla claro y el frontend puede mandar al usuario al login.
func (g *Guard) RequireAPI(next http.Handler) http.Handler {
	return g.proteger(next, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

// proteger concentra la resolución de la sesión; sólo cambia qué se hace
// cuando no hay una válida.
func (g *Guard) proteger(next http.Handler, rechazar http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(NombreCookie)
		if err != nil {
			rechazar(w, r)
			return
		}
		userID, ok, err := g.sesiones.Resolver(r.Context(), c.Value)
		if err != nil {
			// Distinto de "no hay sesión": acá la base falló de verdad. Sin
			// este log, un SQLite caído se ve desde afuera como un bucle de
			// redirección a /login sin ninguna pista de la causa real.
			log.Printf("auth: resolviendo sesión: %v", err)
			rechazar(w, r)
			return
		}
		if !ok {
			rechazar(w, r)
			return
		}
		// La sesión puede sobrevivir a su usuario si la fila se borró sin
		// pasar por el CASCADE; verificarlo evita servir una sesión huérfana.
		u, err := g.usuarios.PorID(r.Context(), userID)
		if err != nil {
			rechazar(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claveUsuario, u)))
	})
}

// UsuarioDe recupera el usuario que el middleware dejó en el contexto.
func UsuarioDe(ctx context.Context) (*cuenta.Usuario, bool) {
	u, ok := ctx.Value(claveUsuario).(*cuenta.Usuario)
	return u, ok
}

// PonerCookie emite la cookie de sesión. El TTL sale de g.sesiones.TTL(), no
// se recibe por parámetro: así hay una sola fuente de verdad y la cookie no
// puede caducar en un momento distinto de la fila en la base.
func (g *Guard) PonerCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     NombreCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,                 // inaccesible desde JavaScript
		SameSite: http.SameSiteLaxMode, // mitiga CSRF en navegación cruzada
		Secure:   g.cookiesSeguras,     // true detrás de HTTPS
		MaxAge:   int(g.sesiones.TTL().Seconds()),
	})
}

// BorrarCookie la expira en el navegador al cerrar sesión.
func (g *Guard) BorrarCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     NombreCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   g.cookiesSeguras,
		MaxAge:   -1,
	})
}
