package web

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
)

//go:embed templates/*.html
var archivosPlantillas embed.FS

// plantillas guarda un CONJUNTO POR PÁGINA, no todas juntas.
//
// html/template indexa las plantillas por nombre en un espacio único: si
// login.html y register.html definieran ambas "contenido" en el mismo
// conjunto, la segunda pisaría a la primera en silencio y una de las dos
// páginas mostraría el formulario equivocado. Un conjunto por página hace que
// ese error no pueda ocurrir.
//
// template.Must es correcto acá porque las plantillas están embebidas: o
// compilan siempre o no compilan nunca, así que un fallo es un error de
// programación y debe verse al arrancar, no en la primera visita.
var plantillas = map[string]*template.Template{
	"register": template.Must(template.ParseFS(archivosPlantillas, "templates/base.html", "templates/register.html")),
	"login":    template.Must(template.ParseFS(archivosPlantillas, "templates/base.html", "templates/login.html")),
	"player":   template.Must(template.ParseFS(archivosPlantillas, "templates/base.html", "templates/player.html")),
}

// datosFormulario re-renderiza un formulario conservando lo tipeado.
//
// No tiene campo de contraseña a propósito: devolverla al cliente la dejaría
// en el HTML, en el historial del navegador y en cualquier caché intermedia.
type datosFormulario struct {
	Nombre string
	Email  string
	Error  string
	Campo  string // qué campo falló, para poder resaltarlo
}

type datosPlayer struct {
	Usuario *cuenta.Usuario
}

// mensajeCredenciales es idéntico para email inexistente y contraseña
// incorrecta: distinguirlos convertiría el login en un verificador de qué
// cuentas existen.
const mensajeCredenciales = "Email o contraseña incorrectos."

type manejadorPaginas struct {
	usuarios *cuenta.Store
	sesiones *auth.Sessions
	guard    *auth.Guard
	log      *log.Logger
}

// render escribe la página. Renderiza a un buffer ANTES de tocar el
// ResponseWriter: si la plantilla fallara a mitad de camino, escribiendo
// directo ya se habrían mandado el 200 y media página, y el usuario vería HTML
// cortado sin ningún error.
func (p *manejadorPaginas) render(w http.ResponseWriter, pagina string, codigo int, datos any) {
	t, ok := plantillas[pagina]
	if !ok {
		p.log.Printf("web: plantilla desconocida %q", pagina)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", datos); err != nil {
		p.log.Printf("web: renderizando %q: %v", pagina, err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(codigo)
	buf.WriteTo(w)
}

// raiz manda a donde corresponda según haya sesión o no.
func (p *manejadorPaginas) raiz(w http.ResponseWriter, r *http.Request) {
	destino := "/login"
	if c, err := r.Cookie(auth.NombreCookie); err == nil {
		// El tercer valor NO se ignora: distingue "no hay sesión" de "la base
		// falló". Sin registrarlo, un SQLite caído se vería desde afuera como
		// un bucle de redirección sin una sola pista de la causa.
		if _, ok, err := p.sesiones.Resolver(r.Context(), c.Value); err != nil {
			p.log.Printf("web: resolviendo sesión en la raíz: %v", err)
		} else if ok {
			destino = "/player"
		}
	}
	http.Redirect(w, r, destino, http.StatusFound)
}

func (p *manejadorPaginas) registroForm(w http.ResponseWriter, r *http.Request) {
	p.render(w, "register", http.StatusOK, datosFormulario{})
}

// registroEnviar da de alta la cuenta y deja la sesión iniciada.
//
// El alta pasa por cuenta.Registrar y NO por Store.Crear: Crear recibe el hash
// ya calculado y no valida nada, así que desde acá permitiría guardar la
// contraseña en claro y saltarse las reglas de alta.
func (p *manejadorPaginas) registroEnviar(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.render(w, "register", http.StatusUnprocessableEntity,
			datosFormulario{Error: "No se pudo leer el formulario."})
		return
	}
	nombre := r.PostFormValue("nombre")
	email := r.PostFormValue("email")
	clave := r.PostFormValue("contrasena")

	u, err := cuenta.Registrar(r.Context(), p.usuarios, auth.HashPassword, nombre, email, clave)
	if err != nil {
		// Lo tipeado vuelve al formulario —menos la contraseña—: perderlo en
		// cada error es una molestia evitable con dos líneas.
		datos := datosFormulario{Nombre: nombre, Email: email}

		var invalido cuenta.ErrorValidacion
		switch {
		case errors.As(err, &invalido):
			datos.Error, datos.Campo = invalido.Mensaje, invalido.Campo
		case errors.Is(err, cuenta.ErrEmailEnUso):
			datos.Error, datos.Campo = "Ya existe una cuenta con ese email.", "email"
		default:
			p.log.Printf("web: registrando a %q: %v", email, err)
			datos.Error = "No se pudo crear la cuenta. Intentá de nuevo."
			p.render(w, "register", http.StatusInternalServerError, datos)
			return
		}
		p.render(w, "register", http.StatusUnprocessableEntity, datos)
		return
	}

	p.iniciarSesion(w, r, u.ID)
}

func (p *manejadorPaginas) loginForm(w http.ResponseWriter, r *http.Request) {
	p.render(w, "login", http.StatusOK, datosFormulario{})
}

func (p *manejadorPaginas) loginEnviar(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.render(w, "login", http.StatusUnprocessableEntity,
			datosFormulario{Error: "No se pudo leer el formulario."})
		return
	}
	email := r.PostFormValue("email")
	clave := r.PostFormValue("contrasena")

	u, hash, err := p.usuarios.PorEmail(r.Context(), email)
	if err != nil {
		if !errors.Is(err, cuenta.ErrNoEncontrado) {
			p.log.Printf("web: buscando %q: %v", email, err)
			p.render(w, "login", http.StatusInternalServerError,
				datosFormulario{Email: email, Error: "No se pudo iniciar sesión. Intentá de nuevo."})
			return
		}
		// El email no existe. Pagamos igual el costo de bcrypt: sin esto la
		// respuesta volvería en microsegundos mientras que un email registrado
		// tarda ~370 ms, y esa diferencia revela qué cuentas existen aunque el
		// mensaje sea idéntico.
		auth.VerificarEnVacio()
		// El email NO se re-renderiza acá: si lo hiciera, dos intentos fallidos
		// con emails distintos devolverían cuerpos distintos (por el valor
		// tipeado en el campo), y eso delataría lo mismo que el mensaje
		// idéntico intenta ocultar. Por eso datosFormulario va vacío salvo el
		// mensaje genérico.
		p.render(w, "login", http.StatusUnauthorized,
			datosFormulario{Error: mensajeCredenciales})
		return
	}

	if !auth.VerifyPassword(hash, clave) {
		p.render(w, "login", http.StatusUnauthorized,
			datosFormulario{Error: mensajeCredenciales})
		return
	}

	// Rotación de sesión contra session fixation: un token que un atacante
	// haya conseguido fijar en el navegador de la víctima antes del login deja
	// de valer en cuanto el login tiene éxito.
	if err := p.sesiones.DestruirDeUsuario(r.Context(), u.ID); err != nil {
		p.log.Printf("web: rotando la sesión de %d: %v", u.ID, err)
		p.render(w, "login", http.StatusInternalServerError,
			datosFormulario{Email: email, Error: "No se pudo iniciar sesión. Intentá de nuevo."})
		return
	}
	p.iniciarSesion(w, r, u.ID)
}

// iniciarSesion emite la sesión y manda al player. Lo comparten el registro y
// el login para que la cookie se emita en un solo lugar.
func (p *manejadorPaginas) iniciarSesion(w http.ResponseWriter, r *http.Request, userID int64) {
	token, err := p.sesiones.Crear(r.Context(), userID)
	if err != nil {
		p.log.Printf("web: creando la sesión de %d: %v", userID, err)
		http.Error(w, "no se pudo iniciar sesión", http.StatusInternalServerError)
		return
	}
	// PonerCookie toma el TTL de Sessions: una sola fuente de verdad para que
	// la cookie y la fila en la base no caduquen en momentos distintos.
	p.guard.PonerCookie(w, token)
	http.Redirect(w, r, "/player", http.StatusFound)
}

// logout borra la sesión de la base y expira la cookie.
//
// Sólo por POST, y eso lo garantiza el router al registrar únicamente
// "POST /logout": con un GET, un <img src="/logout"> en cualquier página
// cerraría la sesión del visitante.
func (p *manejadorPaginas) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.NombreCookie); err == nil {
		// Se borra la FILA, no sólo la cookie: expirar la cookie dejaría el
		// token vivo en la base y utilizable por quien lo hubiera copiado.
		if err := p.sesiones.Destruir(r.Context(), c.Value); err != nil {
			p.log.Printf("web: destruyendo la sesión: %v", err)
		}
	}
	p.guard.BorrarCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// player renderiza la página del reproductor. Llega acá con sesión válida
// porque RequirePage ya la resolvió y dejó al usuario en el contexto.
func (p *manejadorPaginas) player(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UsuarioDe(r.Context())
	if !ok {
		// Inalcanzable si el router aplicó RequirePage. Si algún día alguien
		// registra esta ruta sin el middleware, esto lo convierte en un 500
		// ruidoso en vez de en un nil dereference dentro de la plantilla.
		p.log.Print("web: /player sin usuario en el contexto: ¿falta RequirePage?")
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	p.render(w, "player", http.StatusOK, datosPlayer{Usuario: u})
}
