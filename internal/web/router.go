// Package web expone el livestream y las cuentas por HTTP.
//
// Es el único paquete que conoce a la vez el motor, el hub y la autenticación:
// hls no sabe que existe HTTP, y viewers no sabe que existe hls.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"log"
	"net/http"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/hls"
	"zapping-live/internal/viewers"
)

// Los estáticos viajan DENTRO del binario. Así el contenedor no necesita
// copiarlos ni depender de cuál sea el directorio de trabajo, y el binario
// estático es de verdad autosuficiente.
//
//go:embed static
var archivosEstaticos embed.FS

// Deps son las piezas que el router necesita. Se reciben ya construidas para
// que el cableado viva en un solo lugar (cmd/server) y este paquete se pueda
// probar sin levantar el proceso entero.
type Deps struct {
	Motor    *hls.Engine
	Pool     *hls.Pool
	Hub      *viewers.Hub
	Guard    *auth.Guard
	Sesiones *auth.Sessions
	Usuarios *cuenta.Store

	// Salud comprueba que las dependencias del proceso responden. Es una
	// función y no el *sql.DB para que este paquete no tenga que importar
	// database/sql por una sola línea; en producción es db.PingContext.
	Salud func(context.Context) error

	Log *log.Logger
}

// NewRouter arma el árbol de rutas.
//
// Usa el routing nativo de net/http (Go 1.22+): patrones con método y con
// comodines nombrados, sin framework. Un beneficio concreto: registrar sólo
// "POST /logout" hace que un GET a esa ruta devuelva 405 sin escribir nada.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", salud(d.Salud))
	mux.Handle("GET /static/", cacheDeAssets(http.FileServerFS(archivosEstaticos)))

	pg := &manejadorPaginas{usuarios: d.Usuarios, sesiones: d.Sesiones, guard: d.Guard, log: d.Log}

	// "GET /{$}" casa la raíz EXACTA. Sin el {$}, "GET /" sería el comodín que
	// atrapa cualquier ruta no registrada y toda URL inexistente terminaría
	// redirigiendo al login en vez de dar 404.
	mux.HandleFunc("GET /{$}", pg.raiz)
	mux.HandleFunc("GET /register", pg.registroForm)
	mux.HandleFunc("POST /register", pg.registroEnviar)
	mux.HandleFunc("GET /login", pg.loginForm)
	mux.HandleFunc("POST /login", pg.loginEnviar)

	// Registrar SÓLO el POST hace que un GET /logout devuelva 405 sin escribir
	// una línea: con un GET, un <img src="/logout"> cerraría sesiones ajenas.
	mux.Handle("POST /logout", d.Guard.RequirePage(http.HandlerFunc(pg.logout)))
	mux.Handle("GET /player", d.Guard.RequirePage(http.HandlerFunc(pg.player)))

	st := &manejadorStream{motor: d.Motor, pool: d.Pool, log: d.Log}

	// Rutas HERMANAS, y eso es un contrato, no un gusto: las URI del .m3u8 son
	// relativas ("segments/segmentN.ts"). Servir el playlist desde otra ruta
	// rompe esos enlaces y da 404 silenciosos.
	mux.Handle("GET /live/stream.m3u8", d.Guard.RequireAPI(http.HandlerFunc(st.playlist)))
	mux.Handle("GET /live/segments/{name}", d.Guard.RequireAPI(http.HandlerFunc(st.segmento)))

	ev := &manejadorEventos{hub: d.Hub, log: d.Log}
	mux.Handle("GET /live/events", d.Guard.RequireAPI(http.HandlerFunc(ev.sse)))

	// El orden importa: registrar por fuera para que la línea de log exista
	// también cuando el handler entra en pánico y recuperar lo atrapa.
	return registrar(d.Log, recuperar(d.Log, mux))
}

// salud responde el healthcheck de Docker.
//
// Toca la base a propósito: un 200 incondicional haría que Docker reiniciara
// contenedores sanos y dejara vivos los rotos.
func salud(comprobar func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if comprobar != nil {
			if err := comprobar(r.Context()); err != nil {
				http.Error(w, "no disponible", http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	}
}

// etagsEstaticos guarda la huella del contenido de cada estático, calculada una
// sola vez al arrancar.
//
// Hace falta porque embed.FS no ofrece NINGUNA otra forma de revalidar: sus
// archivos reportan ModTime cero, y http.ServeContent omite Last-Modified
// cuando la fecha es cero. Sin ETag ni Last-Modified, un `max-age` deja al
// navegador con la copia vieja hasta que expire, sin manera de preguntar si
// cambió. Es exactamente lo que pasó: un arreglo del player desplegado y
// funcionando, invisible durante una hora para quien ya había abierto la página.
var etagsEstaticos = calcularEtags()

func calcularEtags() map[string]string {
	m := make(map[string]string)
	fs.WalkDir(archivosEstaticos, ".", func(ruta string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		datos, err := archivosEstaticos.ReadFile(ruta)
		if err != nil {
			return err
		}
		suma := sha256.Sum256(datos)
		// Comillas incluidas: es la sintaxis que exige la RFC 9110 para un ETag
		// fuerte, y sin ellas los intermediarios lo descartan.
		m["/"+ruta] = `"` + hex.EncodeToString(suma[:16]) + `"`
		return nil
	})
	return m
}

// cacheDeAssets hace que el navegador revalide los estáticos en cada carga.
//
// `no-cache` no significa "no cachear": significa "guárdalo, pero pregunta antes
// de usarlo". Con el ETag puesto, esa pregunta se responde con un 304 de unos
// pocos cientos de bytes, así que hls.js —543 KB— se baja una sola vez igual.
//
// La alternativa sería un `max-age` largo con la huella del contenido en el
// nombre del archivo. Eso obligaría a reescribir las URL en las plantillas al
// vuelo; para tres archivos, revalidar sale más barato y no puede quedar
// desincronizado.
func cacheDeAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag, ok := etagsEstaticos[r.URL.Path]; ok {
			// Se pone ANTES de delegar: http.ServeContent lee el ETag de la
			// cabecera ya escrita para resolver el If-None-Match y devolver 304.
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// respuestaObservada recuerda el código de estado para poder registrarlo:
// http.ResponseWriter no lo expone, hay que interceptar WriteHeader.
type respuestaObservada struct {
	http.ResponseWriter
	codigo int
}

func (r *respuestaObservada) WriteHeader(codigo int) {
	r.codigo = codigo
	r.ResponseWriter.WriteHeader(codigo)
}

// Flush reexpone el Flusher del writer envuelto. Sin esto el SSE se rompería:
// ese handler hace una aserción a http.Flusher y, envuelto, fallaría — y sólo
// en producción, donde los middlewares están puestos.
func (r *respuestaObservada) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom reexpone el io.ReaderFrom del writer envuelto, por la misma razón
// que Flush y con la misma consecuencia invisible en los tests del handler
// aislado: http.ServeContent pregunta por esta interfaz para entregar el .ts
// con sendfile, sin copiarlo por el espacio de usuario. Al embeber la
// INTERFAZ http.ResponseWriter sólo se promueven sus tres métodos, así que sin
// esto cada segmento pasaría por un buffer de 32 KB en RAM que no hace falta.
func (r *respuestaObservada) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

// registrar deja una línea por petición.
func registrar(l *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 por defecto: un handler que sólo llama a Write nunca pasa por
		// WriteHeader, y sin este valor inicial se registraría un 0.
		obs := &respuestaObservada{ResponseWriter: w, codigo: http.StatusOK}
		next.ServeHTTP(obs, r)
		if l != nil {
			l.Printf("%s %s %d", r.Method, r.URL.Path, obs.codigo)
		}
	})
}

// recuperar impide que un pánico en un handler tumbe el proceso.
//
// No es paranoia de manual: el motor del stream y el hub corren en este mismo
// proceso, así que caerse por un handler significa cortarle el stream a todos
// los espectadores conectados.
func recuperar(l *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// ErrAbortHandler es el pánico con el que net/http aborta una
			// respuesta a propósito, y aparece cada vez que un espectador
			// cierra la pestaña con el SSE abierto. Se reenvía tal cual: el
			// servidor ya lo maneja y registrarlo llenaría el log de ruido.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			if l != nil {
				l.Printf("pánico en %s %s: %v", r.Method, r.URL.Path, v)
			}
			http.Error(w, "error interno", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
