# Web y frontend — Plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Poner el livestream y las cuentas detrás de una aplicación web funcionando: las tres páginas del requisito 2, el playlist y los segmentos protegidos, el panel en vivo por SSE, y un binario que se levanta con `go run ./cmd/server`.

**Architecture:** Tres paquetes nuevos. `config` lee el entorno. `viewers` es un hub SSE con **goroutine de dueño único**: una sola goroutine posee el conjunto de clientes, así que ese mapa no lleva lock y todo lo demás entra por canales. `web` tiene el router, los handlers y los assets embebidos. `cmd/server` cablea todo y apaga ordenadamente. Los paquetes ya existentes (`hls`, `auth`, `cuenta`, `storage`) **no se modifican**.

**Tech Stack:** Go 1.26 stdlib — `net/http` con routing nativo 1.22+, `html/template`, `embed`, `encoding/json`. Frontend: CSS propio y hls.js 1.6.16 vendorizado. Cero dependencias nuevas en `go.mod`.

## Global Constraints

- Módulo `zapping-live`, Go 1.26. **`internal/hls`, `internal/auth`, `internal/cuenta` y `internal/storage` no se tocan.** Si una tarea cree necesitarlo, para y reporta.
- **Cero dependencias nuevas.** `go.mod` sigue con exactamente `modernc.org/sqlite` y `golang.org/x/crypto`. hls.js se vendoriza como archivo, no entra a `go.mod`.
- **`CGO_ENABLED=0 go build ./...` debe compilar** en todo momento: es lo que habilita el binario estático del Dockerfile.
- Comentarios, mensajes de error, nombres de test y textos de la interfaz **en español**. Los comentarios explican **por qué**, no qué.
- Ningún test espera más de 1 segundo de tiempo real. Unos pocos del bloque 04 pagan
  deliberadamente una llamada a bcrypt con costo 12 (~370 ms): es la única forma de probar
  que el camino real de hasheo se ejecuta, y ninguno se acerca al segundo.
- `go vet ./...` limpio y `gofmt -l .` sin salida al cerrar cada tarea.
- Cada tarea termina con su propio commit, mensaje en español.

## Restricciones heredadas de los bloques 02 y 03 — no negociables

Salieron de las revisiones de esos bloques. Están en `docs/06-decisiones.md`, sección "Restricciones que heredan los bloques siguientes". Romperlas rompe cosas que ya funcionan.

1. **`Engine.Run` se llama exactamente una vez** por instancia. Sólo `cmd/server/main.go` lo llama.
2. **El hook `onRotate` corre síncronamente en la goroutine de rotación.** `Hub.Publicar` **no puede bloquear ni entrar en pánico**: si bloquea detiene el stream para todos, si entra en pánico tumba la goroutine del motor. Por eso `Publicar` usa `select`/`default`.
3. **El playlist se sirve en `/live/stream.m3u8` y los segmentos en `/live/segments/{name}`.** Las URI dentro del `.m3u8` son relativas (`segments/segmentN.ts`); cualquier otra disposición da 404 silenciosos.
4. **`Snapshot.Window` y `Snapshot.Playlist` son de sólo lectura.** Comparten el array subyacente entre todos los lectores.
5. **El alta va por `cuenta.Registrar`, nunca por `Store.Crear`.** `Crear` no valida y recibe el hash ya hecho: desde un handler permitiría guardar la contraseña en claro.
6. **El handler de login DEBE llamar a `auth.VerificarEnVacio()` cuando el email no existe.** Sin eso el tiempo de respuesta revela qué cuentas existen. La Task 5 tiene un test que lo verifica.
7. **El login rota la sesión** con `Sessions.DestruirDeUsuario` antes de `Sessions.Crear`. Decisión confirmada por el usuario: se acepta que desconecte los otros dispositivos.
8. **`Sessions.Limpiar` debe correr en una goroutine periódica cancelable por contexto** (Task 8), o la tabla `sessions` crece sin límite.
9. **`Sessions.Resolver` devuelve `(int64, bool, error)`.** El tercer valor no se ignora nunca.
10. **La cookie se emite con `Guard.PonerCookie(w, token)`**, que toma el TTL de `Sessions`. No pasar el TTL por separado.

## Cinco desviaciones de `docs/04-web-y-frontend.md`, ya decididas

El documento 04 se escribió antes de que existieran los bloques 02 y 03. **La Task 9 lo actualiza** para que no quede desfasado.

1. **Assets embebidos, no en disco.** El doc situaba los templates y el CSS en `web/static/` en la raíz. `embed` no puede subir de directorio, así que van a `internal/web/templates/` y `internal/web/static/`. Beneficio: el binario es autosuficiente y el Dockerfile del bloque 05 ya no necesita `COPY web/`.
2. **`Hub.Suscribir() (<-chan Evento, func())`.** El doc declaraba `Subscribe() (*client, func())` pero lo usaba como `ch, unsub := ...`. Se corrige a lo segundo: devolver el canal de sólo lectura no filtra el tipo interno.
3. **El adaptador `*hls.Snapshot` → `viewers.Evento` vive en `web`, no en `viewers`.** Así `viewers` no importa `hls` y sigue siendo un hub genérico; `web` es el único paquete que conoce a los dos.
4. **`cmd/server/main.go` e `internal/config` entran en este bloque** (Task 8), no en el 05. Los criterios de aceptación del propio bloque 04 ("el video se reproduce sin cortes", "dos pestañas suben el contador a 2") no son verificables sin arrancar el servidor.
5. **El SSE emite un latido cada 20 s** (`: ping\n\n`). No estaba en el doc. Sin él, un proxy inverso con timeout de inactividad corta la conexión entre rotaciones.

## Estructura de archivos

| Archivo | Responsabilidad |
| --- | --- |
| `internal/config/config.go` | Lectura y validación de variables de entorno |
| `internal/config/config_test.go` | Defaults y valores malformados |
| `internal/viewers/hub.go` | Hub SSE: goroutine de dueño único, backpressure |
| `internal/viewers/hub_test.go` | Contador, cliente lento, desconexión, fuga de goroutines |
| `internal/web/router.go` | `Deps`, `NewRouter`, middleware, `/healthz`, estáticos |
| `internal/web/stream.go` | `.m3u8`, `.ts`, y el adaptador snapshot→evento |
| `internal/web/pages.go` | raíz, registro, login, logout, player |
| `internal/web/events.go` | Endpoint SSE |
| `internal/web/*_test.go` | Los cuatro anteriores, con `httptest` |
| `internal/web/templates/*.html` | `base`, `register`, `login`, `player` |
| `internal/web/static/app.css` | Glassmorfismo |
| `internal/web/static/player.js` | hls.js + `EventSource` + panel |
| `internal/web/static/vendor/hls.min.js` | hls.js 1.6.16 vendorizado |
| `cmd/server/main.go` | Cableado, limpieza de sesiones, apagado ordenado |

**Regla de dependencias:** `web` importa `hls`, `auth`, `cuenta`, `viewers`. `viewers` no importa nada del proyecto. `config` no importa nada del proyecto. Ninguno de `hls`, `cuenta`, `storage` conoce `net/http`.

**Referencia:** [../04-web-y-frontend.md](../04-web-y-frontend.md), [../01-diseno.md](../01-diseno.md), [../06-decisiones.md](../06-decisiones.md).

## Firmas que ya existen y que este bloque consume

Copiadas del código real. No inventar variantes.

```go
// internal/hls
func ParseManifest(path string) (*Pool, error)
func (p *Pool) Resolve(name string) (string, bool)   // lista blanca contra el índice
func (p *Pool) Len() int
func New(p *Pool, opts ...Option) *Engine
func WithWindowSize(n int) Option
func WithRotationHook(fn func(*Snapshot)) Option
func (e *Engine) Current() *Snapshot                 // wait-free, nunca nil
func (e *Engine) Run(ctx context.Context)            // exactamente una vez
type Segment struct { Name string; Duration time.Duration }
type Snapshot struct {
    Seq, DiscSeq int64
    HasDisc      bool
    Window       []Segment  // sólo lectura
    Playlist     []byte     // sólo lectura, .m3u8 ya renderizado
    NextAt       time.Time
}

// internal/storage
func Open(ctx context.Context, path string) (*sql.DB, error)
func Migrate(ctx context.Context, db *sql.DB) error

// internal/storage/storagetest  (sólo para tests)
func AbrirMigrada(t *testing.T) *sql.DB

// internal/cuenta
func NewStore(db *sql.DB) *Store
func (s *Store) PorEmail(ctx context.Context, email string) (*Usuario, string, error) // usuario, hash, err
func (s *Store) PorID(ctx context.Context, id int64) (*Usuario, error)
func NormalizarEmail(raw string) string
func Registrar(ctx context.Context, s *Store, hash func(string) (string, error), name, email, password string) (*Usuario, error)
var ErrEmailEnUso, ErrNoEncontrado error
type ErrorValidacion struct { Campo, Mensaje string }
type Usuario struct { ID int64; Name, Email string; CreatedAt time.Time }

// internal/auth
const NombreCookie = "zapping_session"
func HashPassword(plain string) (string, error)
func VerifyPassword(hash, plain string) bool
func VerificarEnVacio()
func UsuarioDe(ctx context.Context) (*cuenta.Usuario, bool)
func NewSessions(db *sql.DB, ttl time.Duration, opts ...OpcionSesion) *Sessions
func (s *Sessions) Crear(ctx context.Context, userID int64) (string, error)
func (s *Sessions) Destruir(ctx context.Context, token string) error
func (s *Sessions) DestruirDeUsuario(ctx context.Context, userID int64) error
func (s *Sessions) Limpiar(ctx context.Context) (int64, error)
func NewGuard(s *Sessions, u *cuenta.Store, cookiesSeguras bool) *Guard
func (g *Guard) RequirePage(next http.Handler) http.Handler   // sin sesión: 302 a /login
func (g *Guard) RequireAPI(next http.Handler) http.Handler    // sin sesión: 401
func (g *Guard) PonerCookie(w http.ResponseWriter, token string)
func (g *Guard) BorrarCookie(w http.ResponseWriter)
```

---

### Task 1: `internal/config` — configuración por entorno

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nada.
- Produces:
  - `type Config struct { Puerto, RutaDB, DirSegmentos string; TTLSesion time.Duration; CookiesSeguras bool; TamVentana int }`
  - `func Cargar() (Config, error)`

**Por qué falla temprano en vez de caer al default:** un `SESSION_TTL=24` (sin unidad) que silenciosamente se convirtiera en 24 h estaría bien por casualidad; un `SESSION_TTL=media hora` que cayera al default dejaría al operador convencido de haber configurado algo que no configuró. Un valor presente pero ilegible es un error de operación, y el servidor no levanta.

- [ ] **Step 1: Escribir los tests que fallan**

Archivo `internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestCargarDefaults(t *testing.T) {
	// Sin ninguna variable puesta, el servicio debe poder levantar igual.
	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar sin entorno: %v", err)
	}
	if c.Puerto != "8080" {
		t.Errorf("Puerto = %q, quiero \"8080\"", c.Puerto)
	}
	if c.RutaDB != "/data/zapping.db" {
		t.Errorf("RutaDB = %q, quiero \"/data/zapping.db\"", c.RutaDB)
	}
	if c.DirSegmentos != "/app/segments" {
		t.Errorf("DirSegmentos = %q, quiero \"/app/segments\"", c.DirSegmentos)
	}
	if c.TTLSesion != 24*time.Hour {
		t.Errorf("TTLSesion = %v, quiero 24h", c.TTLSesion)
	}
	if c.CookiesSeguras {
		t.Error("CookiesSeguras = true, quiero false: el default es sin HTTPS")
	}
	if c.TamVentana != 3 {
		t.Errorf("TamVentana = %d, quiero 3: el enunciado lo fija", c.TamVentana)
	}
}

func TestCargarLeeElEntorno(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("SEGMENTS_DIR", "/tmp/segs")
	t.Setenv("SESSION_TTL", "45m")
	t.Setenv("SECURE_COOKIES", "true")
	t.Setenv("WINDOW_SIZE", "5")

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar: %v", err)
	}
	if c.Puerto != "9090" || c.RutaDB != "/tmp/x.db" || c.DirSegmentos != "/tmp/segs" {
		t.Errorf("cadenas mal leídas: %+v", c)
	}
	if c.TTLSesion != 45*time.Minute {
		t.Errorf("TTLSesion = %v, quiero 45m", c.TTLSesion)
	}
	if !c.CookiesSeguras {
		t.Error("CookiesSeguras = false, quiero true")
	}
	if c.TamVentana != 5 {
		t.Errorf("TamVentana = %d, quiero 5", c.TamVentana)
	}
}

func TestCargarRechazaValoresIlegibles(t *testing.T) {
	// Un valor presente pero ilegible es un error de operación: preferimos no
	// levantar antes que correr con una configuración que el operador cree
	// haber puesto y en realidad no se aplicó.
	casos := []struct{ variable, valor string }{
		{"SESSION_TTL", "media hora"},
		{"SESSION_TTL", "0"},
		{"SESSION_TTL", "-5m"},
		{"SECURE_COOKIES", "quizás"},
		{"WINDOW_SIZE", "tres"},
		{"WINDOW_SIZE", "0"},
		{"PORT", ""},
	}
	for _, c := range casos {
		t.Run(c.variable+"="+c.valor, func(t *testing.T) {
			t.Setenv(c.variable, c.valor)
			if _, err := Cargar(); err == nil {
				t.Fatalf("quiero error con %s=%q", c.variable, c.valor)
			}
		})
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/config/ -run TestCargar -v`
Expected: FAIL — el paquete no existe todavía.

- [ ] **Step 3: Implementar `internal/config/config.go`**

```go
// Package config lee la configuración del entorno.
//
// Todo tiene default: el servicio levanta sin configurar nada, que es lo que
// hace que `docker run` funcione con un solo comando.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config es la configuración efectiva del servidor.
type Config struct {
	Puerto         string        // PORT
	RutaDB         string        // DB_PATH
	DirSegmentos   string        // SEGMENTS_DIR
	TTLSesion      time.Duration // SESSION_TTL
	CookiesSeguras bool          // SECURE_COOKIES
	TamVentana     int           // WINDOW_SIZE
}

// Cargar arma la configuración desde el entorno.
//
// Una variable AUSENTE cae al default; una variable PRESENTE pero ilegible es
// un error. La distinción importa: caer al default ante un valor mal escrito
// dejaría al operador convencido de haber configurado algo que nunca se
// aplicó, y el síntoma aparecería mucho después y en otro lado.
func Cargar() (Config, error) {
	c := Config{
		Puerto:       "8080",
		RutaDB:       "/data/zapping.db",
		DirSegmentos: "/app/segments",
		TTLSesion:    24 * time.Hour,
		// Default false: sin HTTPS por delante, una cookie Secure no viaja y
		// nadie podría iniciar sesión. En producción va detrás de un proxy TLS
		// y esto pasa a true.
		CookiesSeguras: false,
		// 3 lo fija el enunciado: "3 segmentos (30 segundos) por request".
		TamVentana: 3,
	}

	if v, ok := os.LookupEnv("PORT"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("PORT está definido pero vacío")
		}
		c.Puerto = v
	}
	if v, ok := os.LookupEnv("DB_PATH"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("DB_PATH está definido pero vacío")
		}
		c.RutaDB = v
	}
	if v, ok := os.LookupEnv("SEGMENTS_DIR"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("SEGMENTS_DIR está definido pero vacío")
		}
		c.DirSegmentos = v
	}
	if v, ok := os.LookupEnv("SESSION_TTL"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("SESSION_TTL=%q no es una duración válida (ejemplos: 24h, 45m): %w", v, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("SESSION_TTL=%q debe ser positivo", v)
		}
		c.TTLSesion = d
	}
	if v, ok := os.LookupEnv("SECURE_COOKIES"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SECURE_COOKIES=%q no es booleano (true/false): %w", v, err)
		}
		c.CookiesSeguras = b
	}
	if v, ok := os.LookupEnv("WINDOW_SIZE"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("WINDOW_SIZE=%q no es un entero: %w", v, err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("WINDOW_SIZE=%q debe ser positivo", v)
		}
		c.TamVentana = n
	}

	return c, nil
}
```

- [ ] **Step 4: Correr los tests y verificar que pasan**

Run: `go test ./internal/config/ -v`
Expected: PASS, 3 tests (el tercero con 7 subtests).

- [ ] **Step 5: Verificar formato y vet, y commitear**

```bash
gofmt -l . && go vet ./... && go build ./...
git add internal/config
git commit -m "feat(config): leer la configuración del entorno con defaults

Una variable ausente cae al default; una presente pero ilegible aborta el
arranque. Caer al default ante un valor mal escrito dejaría al operador
convencido de haber configurado algo que nunca se aplicó."
```

---

### Task 2: `internal/viewers` — el hub SSE

**Files:**
- Create: `internal/viewers/hub.go`
- Test: `internal/viewers/hub_test.go`

**Interfaces:**
- Consumes: nada del proyecto. Este paquete es genérico a propósito: no conoce `hls` ni `net/http`.
- Produces:
  - `type Evento struct { Espectadores int64 \`json:"viewers"\`; Secuencia int64 \`json:"sequence"\`; Ventana []string \`json:"window"\`; ProximaEnMs int64 \`json:"nextRotationMs"\`; Discontinuidad bool \`json:"discontinuity"\` }`
  - `func NewHub() *Hub`
  - `func (h *Hub) Run(ctx context.Context)`
  - `func (h *Hub) Suscribir() (<-chan Evento, func())`
  - `func (h *Hub) Publicar(e Evento)`
  - `func (h *Hub) Espectadores() int64`

**El patrón, y por qué:** una **sola goroutine** (`Run`) es dueña del conjunto de clientes. Ese mapa no lleva mutex porque nadie más lo toca; todo lo demás llega por canales. Es el patrón que Go recomienda para estado compartido con muchos escritores y hace que el paquete sea verificable con `-race` sin ambigüedad.

**Backpressure — el punto crítico del bloque.** Hay **dos** lugares donde no se puede bloquear, y son distintos:

1. `Publicar` lo llama el hook de rotación del motor, **síncronamente en la goroutine que hace avanzar el stream**. Si bloqueara, el stream se detendría para todos. Por eso escribe en `difundir` con `select`/`default`.
2. El envío a cada cliente. Un espectador con la red lenta deja de leer su canal; sin `default`, ese único cliente congelaría el broadcast para todos y las goroutines se acumularían. Es la fuga de memoria clásica de este patrón, y es exactamente lo que el criterio de RAM del enunciado busca ver resuelto.

Descartar un evento no cuesta nada real: el siguiente llega en segundos y **trae el estado completo**, no un delta. Esa es la propiedad que hace legítimo el descarte.

**Por qué el hub recuerda el último evento:** al conectarse, un cliente nuevo tendría que esperar hasta la próxima rotación (hasta 10 s) para ver algo. El dueño difunde el último estado conocido en cuanto lo registra, y como el recién llegado ya está en el conjunto, esa única difusión lo alcanza a él y de paso le lleva el contador nuevo al resto. El mismo valor sirve para reconstruir el evento cuando lo único que cambió es el número de espectadores.

**Por qué `Suscribir` espera a que el alta se procese:** cuando vuelve, el cliente ya está en el conjunto, `Espectadores()` ya lo cuenta y el estado vigente ya está en su canal. Sin esa espera, quien se suscribe corre contra la goroutine dueña: leería un contador que todavía no lo incluye, o vería el evento de alta llegar tarde, detrás de otro publicado después. Cuesta un viaje de ida y vuelta por espectador —no por rotación— y elimina toda esa clase de carreras.

- [ ] **Step 1: Escribir los tests que fallan**

Archivo `internal/viewers/hub_test.go`:

```go
package viewers

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// esperarA reintenta hasta que la condición se cumple o se agota el plazo.
//
// Suscribir es síncrono —cuando vuelve, el alta ya está hecha— pero la baja y
// la difusión no lo son: afirmar sobre ellas inmediatamente sería una carrera
// contra la goroutine dueña.
func esperarA(t *testing.T, motivo string, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(time.Second)
	for time.Now().Before(limite) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("se agotó el plazo esperando: %s", motivo)
}

func TestHubCuentaEspectadores(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	if got := h.Espectadores(); got != 0 {
		t.Fatalf("Espectadores() inicial = %d, quiero 0", got)
	}

	_, salir1 := h.Suscribir()
	esperarA(t, "un espectador", func() bool { return h.Espectadores() == 1 })

	_, salir2 := h.Suscribir()
	esperarA(t, "dos espectadores", func() bool { return h.Espectadores() == 2 })

	salir1()
	esperarA(t, "vuelta a un espectador", func() bool { return h.Espectadores() == 1 })

	salir2()
	esperarA(t, "vuelta a cero", func() bool { return h.Espectadores() == 0 })
}

func TestHubDifundeATodos(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	a, salirA := h.Suscribir()
	defer salirA()
	b, salirB := h.Suscribir()
	defer salirB()

	// Cada suscripción provoca un evento de "cambió el contador"; los
	// vaciamos para que el siguiente Publicar sea inequívoco.
	drenar(a)
	drenar(b)

	h.Publicar(Evento{Secuencia: 42, Ventana: []string{"segment0.ts"}})

	for nombre, ch := range map[string]<-chan Evento{"a": a, "b": b} {
		select {
		case e := <-ch:
			if e.Secuencia != 42 {
				t.Errorf("%s: Secuencia = %d, quiero 42", nombre, e.Secuencia)
			}
			if e.Espectadores != 2 {
				t.Errorf("%s: Espectadores = %d, quiero 2: el hub completa el contador", nombre, e.Espectadores)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s no recibió el evento", nombre)
		}
	}
}

func TestHubClienteLentoNoBloqueaALosDemas(t *testing.T) {
	// El test que respalda la afirmación sobre manejo de RAM: un cliente que
	// deja de leer no puede congelar el broadcast ni acumular eventos sin
	// límite. Si el envío no tuviera `default`, este test colgaría.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	// Sin defer de las funciones de baja a propósito: el `defer cancelar()` de
	// arriba ya cierra los canales al apagar el hub. Si además se difiriera la
	// baja, un t.Fatal en este test la ejecutaría ANTES que cancelar (los defer
	// corren en orden inverso) y quedaría bloqueada mandando a un Run detenido:
	// el fallo se reportaría como un cuelgue de diez minutos en vez de una
	// línea FAIL.
	lento, _ := h.Suscribir() // nunca se lee: es el cliente atascado
	rapido, _ := h.Suscribir()

	// El cliente rápido necesita un lector ACTIVO. Sin él su buffer se llenaría
	// igual que el del lento, y el test no distinguiría "el hub descartó por
	// lento" de "el hub se detuvo" — que es justamente lo que viene a separar.
	var ultimoVisto atomic.Int64
	go func() {
		for e := range rapido {
			ultimoVisto.Store(e.Secuencia)
		}
	}()

	// Muchos más eventos que la capacidad del buffer por cliente.
	const n = 500
	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < n; i++ {
			h.Publicar(Evento{Secuencia: int64(i)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(2 * time.Second):
		t.Fatal("Publicar se bloqueó: un cliente lento está frenando al motor")
	}

	// El buffer del lento está acotado: no puede haber tragado los 500.
	if l := len(lento); l > capacidadCliente {
		t.Errorf("el cliente lento acumuló %d eventos, el tope es %d", l, capacidadCliente)
	}

	// Y el hub sigue aceptando y entregando. Se republica en cada intento
	// porque Publicar descarta cuando el canal de difusión está lleno, y tras
	// una ráfaga de 500 puede estarlo: un único intento haría el test
	// intermitente por la razón equivocada.
	esperarA(t, "que el cliente rápido siga recibiendo", func() bool {
		h.Publicar(Evento{Secuencia: 9999})
		return ultimoVisto.Load() == 9999
	})
}

func TestHubDesconexionNoDejaGoroutines(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()

	// Run arranca envuelto en un canal propio para poder afirmar sobre ESTA
	// goroutine. runtime.NumGoroutine() es global al proceso: los tests
	// anteriores dejan sus Run drenando, así que un descenso del contador
	// puede venir de una goroutine ajena terminando mientras esta sigue viva.
	// Con `corriendo` la comprobación es directa y no admite esa confusión.
	corriendo := make(chan struct{})
	go func() {
		defer close(corriendo)
		h.Run(ctx)
	}()
	esperarA(t, "que el hub arranque", func() bool { return h.Espectadores() == 0 })

	// Alta y baja repetidas no deben acumular nada. Acá NumGoroutine() sí
	// sirve: nada en este bucle crea goroutines, así que un aumento sólo puede
	// venir del hub.
	base := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		_, salir := h.Suscribir()
		salir()
	}
	esperarA(t, "que se den de baja todos", func() bool { return h.Espectadores() == 0 })
	if n := runtime.NumGoroutine(); n > base {
		t.Errorf("suscribir y dar de baja dejó goroutines: %d, empezó en %d", n, base)
	}

	cancelar()
	select {
	case <-corriendo:
	case <-time.After(time.Second):
		t.Fatal("Run no volvió tras cancelar el contexto")
	}
}

func TestApagarCierraLosCanalesDeLosClientes(t *testing.T) {
	// Es lo que hace que los handlers SSE salgan de su bucle solos al apagar el
	// proceso. Sin esto, cada espectador conectado dejaría una goroutine
	// esperando eventos que ya no van a llegar, y http.Server.Shutdown agotaría
	// su plazo completo esperando conexiones que no se cierran nunca.
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()
	go h.Run(ctx)

	// Deliberadamente SIN llamar a salir(): lo que se prueba es que el apagado
	// cierra el canal por su cuenta. Si el test se diera de baja primero, el
	// canal lo cerraría la rama de baja y esta comprobación no probaría nada.
	ch, _ := h.Suscribir()
	cancelar()

	plazo := time.After(time.Second)
	for {
		select {
		case _, abierto := <-ch:
			if !abierto {
				return // el canal se cerró: es exactamente lo que se busca
			}
			// Eventos pendientes en el buffer: seguir drenando hasta el cierre.
		case <-plazo:
			t.Fatal("el canal del cliente no se cerró al apagar el hub: el handler SSE quedaría colgado")
		}
	}
}

func TestPublicarNoBloqueaConElHubDetenido(t *testing.T) {
	// LA garantía que sostiene todo el diseño: Publicar lo llama el hook de
	// rotación del motor, síncronamente en la goroutine que hace avanzar el
	// stream. Si se bloqueara, el stream se detendría para TODOS.
	//
	// Se prueba en aislamiento y sin arrancar Run: así nadie drena `difundir`,
	// y basta con superar su capacidad para que el select/default sea lo único
	// que separa esta función de un bloqueo permanente. Probarlo con Run
	// corriendo no serviría — el hub vacía el canal en microsegundos y un
	// Publicar bloqueante pasaría igual.
	h := NewHub() // Run nunca se llama

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < capacidadDifusion*10; i++ {
			h.Publicar(Evento{Secuencia: int64(i)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(time.Second):
		t.Fatal("Publicar se bloqueó con el hub detenido: eso frenaría la goroutine de rotación del motor")
	}
}

func TestSalirEsIdempotente(t *testing.T) {
	// El handler SSE llama a salir() con defer, pero también podría llamarlo
	// en una rama de error: dos veces no debe descontar dos espectadores.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	_, salir := h.Suscribir()
	_, quedarse := h.Suscribir()
	defer quedarse()
	esperarA(t, "dos espectadores", func() bool { return h.Espectadores() == 2 })

	salir()
	salir()
	salir()
	esperarA(t, "un espectador", func() bool { return h.Espectadores() == 1 })

	// Un tercer valor distinto de 1 en el próximo medio segundo sería una baja
	// de más colándose tarde.
	time.Sleep(100 * time.Millisecond)
	if got := h.Espectadores(); got != 1 {
		t.Fatalf("Espectadores() = %d tras tres salir(), quiero 1", got)
	}
}

func TestSuscribirTrasApagarNoCuelga(t *testing.T) {
	// Un request SSE puede llegar entre que el contexto se cancela y que el
	// servidor HTTP termina de cerrar. Suscribir no puede quedarse colgado
	// escribiendo en un canal que ya nadie lee.
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()
	go h.Run(ctx)
	cancelar()
	esperarA(t, "que Run termine", func() bool {
		select {
		case <-h.terminado:
			return true
		default:
			return false
		}
	})

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		ch, salir := h.Suscribir()
		salir()
		// El canal ya viene cerrado: leerlo devuelve el cero inmediatamente.
		<-ch
	}()
	select {
	case <-hecho:
	case <-time.After(time.Second):
		t.Fatal("Suscribir se colgó con el hub apagado")
	}
}

func TestHubEntregaElUltimoEstadoAlConectar(t *testing.T) {
	// Sin esto, un espectador que abre la página justo después de una rotación
	// vería el panel vacío hasta 10 segundos.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	primero, salirPrimero := h.Suscribir()
	defer salirPrimero()
	drenar(primero)
	h.Publicar(Evento{Secuencia: 7, Ventana: []string{"segment7.ts"}})
	select {
	case <-primero:
	case <-time.After(time.Second):
		t.Fatal("el primer cliente no recibió el evento")
	}

	segundo, salirSegundo := h.Suscribir()
	defer salirSegundo()
	select {
	case e := <-segundo:
		if e.Secuencia != 7 {
			t.Errorf("Secuencia = %d, quiero 7: el hub debe entregar el último estado al conectar", e.Secuencia)
		}
		if e.Espectadores != 2 {
			t.Errorf("Espectadores = %d, quiero 2", e.Espectadores)
		}
	case <-time.After(time.Second):
		t.Fatal("el cliente nuevo no recibió el estado vigente")
	}
}

func drenar(ch <-chan Evento) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/viewers/ -v`
Expected: FAIL — el paquete no existe.

- [ ] **Step 3: Implementar `internal/viewers/hub.go`**

```go
// Package viewers difunde el estado del stream a los espectadores conectados.
//
// No conoce hls ni net/http: recibe eventos ya armados y los reparte. Esa
// ignorancia deliberada es lo que permite probarlo sin levantar un servidor ni
// arrancar el motor.
package viewers

import (
	"context"
	"sync"
	"sync/atomic"
)

// capacidadCliente es el buffer de eventos por espectador.
//
// Ocho es holgado para el ritmo real (un evento cada ~10 s por rotación, más
// los cambios de contador) y sigue siendo un techo duro: 500 espectadores
// lentos ocupan 500*8 eventos, no memoria sin límite.
const capacidadCliente = 8

// capacidadDifusion amortigua ráfagas entre el motor y la goroutine dueña, para
// que un pico corto no se traduzca en eventos descartados.
const capacidadDifusion = 8

// Evento es el estado que ve el panel del player. Se manda completo, no como
// delta: por eso descartarlo ante un cliente lento es inofensivo — el
// siguiente vuelve a traer todo.
//
// Ventana es de SÓLO LECTURA una vez pasada a Publicar. El hub conserva el
// último evento para entregárselo a quien se conecte, así que ese slice sigue
// vivo mucho después de la llamada y lo comparten todos los espectadores:
// mutarlo corrompería lo que ven todos.
type Evento struct {
	Espectadores   int64    `json:"viewers"`
	Secuencia      int64    `json:"sequence"`
	Ventana        []string `json:"window"`
	ProximaEnMs    int64    `json:"nextRotationMs"`
	Discontinuidad bool     `json:"discontinuity"`
}

type cliente struct {
	ch chan Evento

	// listo lo cierra la goroutine dueña cuando terminó de registrar a este
	// cliente y de difundir su alta. Suscribir lo espera, y con eso ofrece una
	// garantía que vale la pena: cuando vuelve, el cliente YA está en el
	// conjunto, Espectadores() ya lo cuenta, y el estado vigente ya está en su
	// canal. Sin esa espera, quien se suscribe corre contra la goroutine dueña
	// y puede leer un contador viejo o encontrarse el evento de alta llegando
	// tarde, detrás de otro que pidió después.
	listo chan struct{}
}

// Hub reparte eventos a los espectadores conectados.
//
// Una SOLA goroutine (Run) es dueña del conjunto de clientes, así que ese mapa
// no lleva mutex: nadie más lo toca. Todo lo demás entra por canales. El único
// atómico es el contador, y existe sólo para que leerlo desde fuera sea barato.
type Hub struct {
	alta     chan *cliente
	baja     chan *cliente
	difundir chan Evento

	// terminado se cierra cuando Run vuelve. Es lo que permite que Suscribir y
	// la función de baja no se cuelguen si el hub ya se apagó.
	terminado chan struct{}
	cerrarUna sync.Once

	espectadores atomic.Int64
}

// NewHub crea el hub. Todavía no reparte nada: hay que arrancar Run en su
// propia goroutine, y exactamente una vez.
func NewHub() *Hub {
	return &Hub{
		alta:      make(chan *cliente),
		baja:      make(chan *cliente),
		difundir:  make(chan Evento, capacidadDifusion),
		terminado: make(chan struct{}),
	}
}

// Espectadores es el número de conexiones vivas. Lectura atómica: la puede
// llamar cualquiera sin coordinarse con la goroutine dueña.
func (h *Hub) Espectadores() int64 { return h.espectadores.Load() }

// Publicar entrega un evento al hub. NUNCA BLOQUEA.
//
// La llama el hook de rotación del motor, que corre síncronamente en la
// goroutine que hace avanzar el stream: si esta función se bloqueara, el
// stream se detendría para todos los espectadores. Ante un hub saturado se
// descarta el evento, que no cuesta nada porque el siguiente trae el estado
// completo otra vez.
func (h *Hub) Publicar(e Evento) {
	select {
	case h.difundir <- e:
	default:
	}
}

// Suscribir registra un espectador y devuelve su canal y su función de baja.
//
// Cuando vuelve, el alta ya está hecha: el cliente está en el conjunto, el
// contador lo incluye y el estado vigente ya está en su canal. Esa sincronía
// cuesta un viaje de ida y vuelta por evento de suscripción —algo que pasa una
// vez por espectador, no por rotación— y a cambio elimina toda una clase de
// carreras entre quien se suscribe y la goroutine dueña.
//
// El canal es de sólo lectura: el que escribe es siempre la goroutine dueña.
// La función de baja es idempotente y segura de llamar aunque el hub ya se
// haya apagado.
func (h *Hub) Suscribir() (<-chan Evento, func()) {
	c := &cliente{
		ch:    make(chan Evento, capacidadCliente),
		listo: make(chan struct{}),
	}

	select {
	case h.alta <- c:
	case <-h.terminado:
		// El hub ya no corre. Devolvemos un canal cerrado en vez de uno vivo:
		// el handler SSE lo lee, ve que está cerrado y termina de inmediato en
		// vez de quedarse esperando eventos que no van a llegar nunca.
		close(c.ch)
		return c.ch, func() {}
	}

	// El alta ya fue aceptada; falta que la goroutine dueña termine de
	// procesarla.
	//
	// Con `alta` sin buffer, que el envío de arriba haya tenido éxito significa
	// que Run ya está dentro del cuerpo del case, y ese cuerpo cierra c.listo
	// sin condiciones: hoy este select siempre sale por la primera rama. La de
	// h.terminado se mantiene porque la garantía depende de que `alta` NO tenga
	// buffer — si alguien se lo agregara, aparecería la ventana en la que Run
	// se apaga con un alta encolada y sin esta rama la espera no volvería nunca.
	select {
	case <-c.listo:
	case <-h.terminado:
	}

	var una sync.Once
	return c.ch, func() {
		una.Do(func() {
			select {
			case h.baja <- c:
			case <-h.terminado:
			}
		})
	}
}

// Run posee el conjunto de clientes hasta que se cancele el contexto.
//
// Debe correr en su propia goroutine y exactamente una vez por Hub: es la
// dueña única de `clientes` y de `ultimo`, y esa exclusividad es lo que
// reemplaza al mutex.
func (h *Hub) Run(ctx context.Context) {
	clientes := make(map[*cliente]struct{})

	// ultimo es el estado más reciente del stream. Se guarda para dárselo al
	// instante a quien se conecta —si no, el panel quedaría vacío hasta la
	// próxima rotación— y para reconstruir el evento cuando lo único que
	// cambió fue el número de espectadores.
	var ultimo Evento

	defer func() {
		// Cerrar los canales hace que los handlers SSE salgan de su bucle solos.
		for c := range clientes {
			close(c.ch)
		}
		h.espectadores.Store(0)
		h.cerrarUna.Do(func() { close(h.terminado) })
	}()

	for {
		select {
		case c := <-h.alta:
			clientes[c] = struct{}{}
			ultimo.Espectadores = int64(len(clientes))
			h.espectadores.Store(ultimo.Espectadores)
			// Una sola difusión, no dos: el recién llegado YA está en el
			// conjunto, así que difundirA le entrega el estado vigente al
			// mismo tiempo que al resto el contador nuevo. Mandárselo aparte
			// además de esto le entregaría el mismo evento dos veces.
			difundirA(clientes, ultimo)
			// Recién ahora Suscribir puede volver: el cliente está registrado,
			// contado y con el estado vigente en su canal.
			close(c.listo)

		case c := <-h.baja:
			// Hoy es inalcanzable: el sync.Once de Suscribir ya garantiza que
			// cada cliente llegue acá una sola vez. Se deja porque es la guarda
			// que hace que el invariante viva en el dueño del estado y no sólo
			// en el llamante: sin ella, cualquier futura vía de baja que no
			// pase por ese Once descontaría de más y cerraría un canal ya
			// cerrado, que es un pánico.
			if _, existe := clientes[c]; !existe {
				continue
			}
			delete(clientes, c)
			close(c.ch)
			ultimo.Espectadores = int64(len(clientes))
			h.espectadores.Store(ultimo.Espectadores)
			difundirA(clientes, ultimo)

		case e := <-h.difundir:
			// El contador lo pone el hub, no el llamante: es el único que lo sabe.
			e.Espectadores = int64(len(clientes))
			ultimo = e
			h.espectadores.Store(e.Espectadores)
			difundirA(clientes, e)

		case <-ctx.Done():
			return
		}
	}
}

// difundirA reparte el evento sin bloquear en ningún cliente.
func difundirA(clientes map[*cliente]struct{}, e Evento) {
	for c := range clientes {
		enviar(c, e)
	}
}

// enviar deposita el evento si hay lugar y lo DESCARTA si no.
//
// Sin el `default`, un solo espectador que dejó de leer —red lenta, pestaña
// congelada— bloquearía a la goroutine dueña y con ella el broadcast a todos
// los demás, mientras los eventos se acumulan sin techo. Es la fuga de memoria
// clásica de este patrón. Descartar es correcto porque cada evento trae el
// estado completo: el siguiente lo pone al día igual.
func enviar(c *cliente, e Evento) {
	select {
	case c.ch <- e:
	default:
	}
}
```

- [ ] **Step 4: Correr los tests y verificar que pasan**

Run: `go test ./internal/viewers/ -count=5 ./internal/viewers/`
Expected: PASS, 9 tests, sin intermitencia entre repeticiones.

Los tres tests que existen para que ciertas mutaciones no pasen desapercibidas:
`TestPublicarNoBloqueaConElHubDetenido` (quitar el `default` de `Publicar`),
`TestApagarCierraLosCanalesDeLosClientes` (borrar el cierre de canales del
apagado), y el `select` sobre `corriendo` de `TestHubDesconexionNoDejaGoroutines`
(hacer que `Run` no vuelva al cancelar). Antes ninguna de las tres rompía nada.

- [ ] **Step 5: Verificar que no hay carreras**

`-race` no corre en Windows sin compilador C; se verifica en contenedor Linux. Si Docker no está disponible, anotarlo y seguir — la Task 9 lo recoge.

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "C:/Users/Matias/Desktop/PruebaTecnica:/src" -w /src \
  golang:1.26 go test -race -count=1 ./internal/viewers/
```
Expected: `ok`, sin advertencias de carrera.

- [ ] **Step 6: Commitear**

```bash
gofmt -l . && go vet ./... && go build ./...
git add internal/viewers
git commit -m "feat(viewers): hub SSE con goroutine dueña única y backpressure

Una sola goroutine posee el conjunto de clientes, así que ese mapa no lleva
lock. Hay dos puntos donde no se puede bloquear y son distintos: Publicar,
porque lo llama el hook síncrono del motor y frenarlo detendría el stream
para todos; y el envío por cliente, porque un espectador lento congelaría el
broadcast y acumularía eventos sin techo. Ambos descartan con select/default.
Descartar es inofensivo: cada evento trae el estado completo, no un delta."
```

---

### Task 3: `internal/web` — router, middleware, `/healthz` y estáticos

**Files:**
- Create: `internal/web/router.go`, `internal/web/static/app.css`
- Test: `internal/web/router_test.go`, `internal/web/webtest_test.go`

**Interfaces:**
- Consumes: `hls.Engine`, `hls.Pool`, `viewers.Hub`, `auth.Guard`, `auth.Sessions`, `cuenta.Store`.
- Produces:
  - `type Deps struct { Motor *hls.Engine; Pool *hls.Pool; Hub *viewers.Hub; Guard *auth.Guard; Sesiones *auth.Sessions; Usuarios *cuenta.Store; Salud func(context.Context) error; Log *log.Logger }`
  - `func NewRouter(d Deps) http.Handler`
  - Helpers de test `banco`, `entorno(t)`, `poolDePrueba(t)`, `usuarioConSesion(t, b)`, `hashBarato`, `bufferDeLog`, que reutilizan las Tasks 4, 5 y 6.

**Por qué `Salud` es una función y no el `*sql.DB`:** el healthcheck tiene que tocar la base, o sería un 200 que no prueba nada. Pero pasar el `*sql.DB` obligaría a `web` a importar `database/sql` para una sola línea. Una `func(context.Context) error` dice exactamente lo que necesita —"¿está viva la dependencia?"— y en `main` se satisface con `db.PingContext`.

**Por qué el `recover` va por dentro del logging:** el middleware de logging tiene que poder registrar la línea de la petición que entró en pánico. Si el `recover` estuviera por fuera, el pánico se comería el log. El orden es `registrar` → `recuperar` → mux.

**Nota para quien implemente:** el `//go:embed templates/*.html` NO va en este archivo — lo agrega la Task 5 junto con los templates. `embed` falla en compilación si el patrón no encuentra ningún archivo.

- [ ] **Step 1: Escribir los helpers de test**

Archivo `internal/web/webtest_test.go`:

```go
package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/hls"
	"zapping-live/internal/storage/storagetest"
	"zapping-live/internal/viewers"
)

// banco es el entorno completo de un test: el router listo para usar más las
// piezas sueltas, por si el test necesita hablarles directamente.
type banco struct {
	Handler  http.Handler
	Motor    *hls.Engine
	Pool     *hls.Pool
	Hub      *viewers.Hub
	Guard    *auth.Guard
	Sesiones *auth.Sessions
	Usuarios *cuenta.Store

	// Registro acumula lo que el middleware de logging escriba durante el test.
	Registro *bufferDeLog
}

// hashBarato reemplaza a auth.HashPassword en los tests.
//
// auth.HashPassword usa costo 12 (~370 ms por llamada, a propósito). Pagarlo
// por cada usuario de prueba haría que la suite tardara minutos. bcrypt.MinCost
// produce un hash igual de válido para VerifyPassword, que no mira el costo.
func hashBarato(plano string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plano), bcrypt.MinCost)
	return string(b), err
}

// entorno arma base migrada, pool de segmentos falsos, motor, hub y router.
//
// El hub corre de verdad: los tests del SSE lo necesitan vivo y a los demás no
// les molesta. Se apaga con t.Cleanup.
func entorno(t *testing.T) *banco {
	t.Helper()

	db := storagetest.AbrirMigrada(t)
	usuarios := cuenta.NewStore(db)
	sesiones := auth.NewSessions(db, time.Hour)
	guard := auth.NewGuard(sesiones, usuarios, false)

	pool := poolDePrueba(t)
	// Sin WithRotationHook y sin Run: Current() ya devuelve un snapshot válido
	// desde New, y arrancar el motor haría los tests dependientes del reloj.
	motor := hls.New(pool, hls.WithWindowSize(3))

	hub := viewers.NewHub()
	ctx, cancelar := context.WithCancel(context.Background())
	go hub.Run(ctx)
	t.Cleanup(cancelar)

	b := &banco{
		Motor: motor, Pool: pool, Hub: hub,
		Guard: guard, Sesiones: sesiones, Usuarios: usuarios,
	}
	// El log va a un buffer y no a stderr: el middleware emite una linea por
	// peticion, y con decenas de tests eso entierra la salida de `go test` en
	// ruido. El buffer queda accesible por si un test necesita afirmar sobre el.
	b.Registro = &bufferDeLog{}
	b.Handler = NewRouter(Deps{
		Motor: motor, Pool: pool, Hub: hub,
		Guard: guard, Sesiones: sesiones, Usuarios: usuarios,
		Salud: func(context.Context) error { return nil },
		Log:   log.New(b.Registro, "test: ", 0),
	})
	return b
}

// poolDePrueba escribe un manifiesto y cuatro .ts diminutos en un directorio
// temporal.
//
// El último dura 4.566667 s a propósito: es la duración real de segment63.ts en
// el material provisto, y el caso que descarta la solución del ticker fijo.
func poolDePrueba(t *testing.T) *hls.Pool {
	t.Helper()
	dir := t.TempDir()

	duraciones := []float64{10, 10, 10, 4.566667}
	var m strings.Builder
	m.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for i, d := range duraciones {
		nombre := fmt.Sprintf("segment%d.ts", i)
		fmt.Fprintf(&m, "#EXTINF:%.6f,\n%s\n", d, nombre)
		// Contenido reconocible: los tests verifican qué bytes salieron.
		cuerpo := []byte("contenido-falso-del-" + nombre)
		if err := os.WriteFile(filepath.Join(dir, nombre), cuerpo, 0o644); err != nil {
			t.Fatalf("escribiendo %s: %v", nombre, err)
		}
	}
	m.WriteString("#EXT-X-ENDLIST\n")

	ruta := filepath.Join(dir, "segment.m3u8")
	if err := os.WriteFile(ruta, []byte(m.String()), 0o644); err != nil {
		t.Fatalf("escribiendo el manifiesto: %v", err)
	}
	p, err := hls.ParseManifest(ruta)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return p
}

// usuarioConSesion da de alta un usuario y devuelve su cookie de sesión.
//
// Usa cuenta.Registrar —no Store.Crear— porque es la única vía que valida y
// hashea, igual que hace el handler de registro.
func usuarioConSesion(t *testing.T, b *banco) (*cuenta.Usuario, *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	u, err := cuenta.Registrar(ctx, b.Usuarios, hashBarato, "Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	token, err := b.Sesiones.Crear(ctx, u.ID)
	if err != nil {
		t.Fatalf("Sesiones.Crear: %v", err)
	}
	return u, &http.Cookie{Name: auth.NombreCookie, Value: token}
}

// bufferDeLog acumula lo que escriba un *log.Logger.
type bufferDeLog struct{ b strings.Builder }

func (l *bufferDeLog) Write(p []byte) (int, error) { return l.b.Write(p) }
func (l *bufferDeLog) String() string              { return l.b.String() }
```

- [ ] **Step 2: Escribir los tests del router**

Archivo `internal/web/router_test.go`:

```go
package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSaludResponde200(t *testing.T) {
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
}

func TestSaludFallaSiLaBaseNoResponde(t *testing.T) {
	// Un healthcheck que devuelve 200 pase lo que pase no sirve de nada:
	// Docker reiniciaría contenedores sanos y dejaría vivos los rotos.
	h := NewRouter(Deps{
		Salud: func(context.Context) error { return errors.New("base caída") },
		Log:   log.New(os.Stderr, "test: ", 0),
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("código = %d, quiero 503", w.Code)
	}
}

func TestEstaticosSeSirvenEmbebidos(t *testing.T) {
	// Van dentro del binario: el contenedor no necesita copiarlos ni depender
	// de cuál sea el directorio de trabajo.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/app.css", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, quiero un max-age", cc)
	}
}

func TestEstaticosNoRequierenSesion(t *testing.T) {
	// El CSS de la página de login tiene que cargar sin sesión.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/app.css", nil))
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusFound {
		t.Fatalf("código = %d: los estáticos no deben estar protegidos", w.Code)
	}
}

func TestRecuperarDevuelve500YNoTumbaElProceso(t *testing.T) {
	// Un pánico en un handler no puede llevarse por delante al resto del
	// servidor: el motor del stream y el hub corren en este mismo proceso, así
	// que caerse significa cortarle el stream a todos los espectadores.
	registro := &bufferDeLog{}
	entraEnPanico := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("explotó a propósito")
	})
	h := recuperar(log.New(registro, "", 0), entraEnPanico)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/lo-que-sea", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d, quiero 500", w.Code)
	}
	if !strings.Contains(registro.String(), "explotó a propósito") {
		t.Errorf("el log no menciona el pánico: %q", registro.String())
	}
}

func TestRecuperarDejaPasarErrAbortHandler(t *testing.T) {
	// net/http usa ErrAbortHandler para abortar una respuesta a propósito, y
	// lo emite cada vez que un espectador cierra la pestaña con el SSE abierto.
	// Tratarlo como error llenaría el log de ruido; el servidor ya lo maneja.
	registro := &bufferDeLog{}
	aborta := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := recuperar(log.New(registro, "", 0), aborta)

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recuperar() se tragó ErrAbortHandler: recover() = %v", v)
		}
		if registro.String() != "" {
			t.Errorf("no debe registrarse nada, se registró: %q", registro.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sse", nil))
	t.Fatal("quiero que el pánico se propague")
}

func TestRegistrarDejaLineaDeLog(t *testing.T) {
	registro := &bufferDeLog{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := registrar(log.New(registro, "", 0), ok)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/ruta/observada", nil))

	linea := registro.String()
	if !strings.Contains(linea, "/ruta/observada") {
		t.Errorf("el log no menciona la ruta: %q", linea)
	}
	if !strings.Contains(linea, "418") {
		t.Errorf("el log no menciona el código de estado: %q", linea)
	}
}

func TestRegistrarUsa200CuandoElHandlerNoLoFija(t *testing.T) {
	// Un handler que sólo llama a Write —como /healthz en su camino sano, que
	// es la petición más frecuente del sistema— nunca pasa por WriteHeader.
	// Sin el valor inicial de respuestaObservada, todas esas peticiones
	// quedarían registradas con un 0.
	registro := &bufferDeLog{}
	soloEscribe := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	h := registrar(log.New(registro, "", 0), soloEscribe)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/solo-write", nil))

	if !strings.Contains(registro.String(), "200") {
		t.Errorf("el log no registró 200 para un handler que sólo escribe: %q", registro.String())
	}
}

func TestElPanicoIgualDejaLineaDeLog(t *testing.T) {
	// El orden de los middlewares es load-bearing: `registrar` tiene que
	// envolver a `recuperar`, no al revés. Invertido, el pánico atravesaría a
	// registrar antes de llegar a su Printf, y la petición que tumbó al handler
	// sería justamente la única sin línea de log — la que más falta hace.
	//
	// Se prueba sobre el router REAL, no sobre una composición armada acá: una
	// composición local seguiría pasando aunque NewRouter invirtiera el orden.
	// El pánico entra por la función de salud, que es la única vía que Deps
	// ofrece para meterlo dentro de una ruta de verdad.
	registro := &bufferDeLog{}
	h := NewRouter(Deps{
		Salud: func(context.Context) error { panic("explotó dentro de una ruta real") },
		Log:   log.New(registro, "", 0),
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d, quiero 500", w.Code)
	}
	linea := registro.String()
	if !strings.Contains(linea, "/healthz") {
		t.Errorf("no quedó línea de log para la petición que entró en pánico: %q", linea)
	}
	if !strings.Contains(linea, "500") {
		t.Errorf("la línea de log no registra el 500: %q", linea)
	}
}

func TestRespuestaObservadaConservaElFlusher(t *testing.T) {
	// El SSE hace una aserción a http.Flusher sobre el ResponseWriter. Si el
	// envoltorio del logging no reexpusiera Flush, esa aserción fallaría y el
	// endpoint devolvería 500 en producción, donde los middlewares sí están
	// puestos — pero pasaría los tests del handler aislado. Este test cierra
	// justo ese hueco.
	var vioFlusher bool
	interior := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, vioFlusher = w.(http.Flusher)
	})
	h := registrar(log.New(&bufferDeLog{}, "", 0), interior)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/live/events", nil))

	if !vioFlusher {
		t.Fatal("el writer envuelto no implementa http.Flusher: el SSE no podría vaciar")
	}
}
```

- [ ] **Step 3: Correr los tests y verificar que fallan**

Run: `go test ./internal/web/ -v`
Expected: FAIL — el paquete no existe todavía.

- [ ] **Step 4: Escribir `internal/web/static/app.css`**

Sólo la base y las variables; la Task 7 agrega los componentes del player.

```css
/* Glassmorfismo propio, sin framework. Ver docs/06-decisiones.md.
   REGLA ESTRICTA: backdrop-filter NUNCA sobre el <video>. */

:root {
  --fondo:       #0b0d12;
  --glass-bg:    rgba(255, 255, 255, .06);
  --glass-borde: rgba(255, 255, 255, .12);
  --texto:       #f2f4f8;
  /* Contraste 7:1 sobre el fondo. El gris suave habitual del glassmorfismo
     (#8a8f98) queda en 3.4:1 y no pasa AA; este es el punto donde este estilo
     suele fallar. */
  --texto-suave: #b9c0cc;
  --acento:      #e50914;
  --error:       #ff8a8a;
  --radio:       16px;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  min-height: 100vh;
  background:
    radial-gradient(60rem 60rem at 15% -10%, rgba(229, 9, 20, .22), transparent 60%),
    radial-gradient(50rem 50rem at 95% 10%, rgba(64, 120, 255, .18), transparent 55%),
    var(--fondo);
  background-attachment: fixed;
  color: var(--texto);
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  font-size: 16px;
  line-height: 1.5;
}

.vidrio {
  background: var(--glass-bg);
  backdrop-filter: blur(14px) saturate(140%);
  -webkit-backdrop-filter: blur(14px) saturate(140%);
  border: 1px solid var(--glass-borde);
  border-radius: var(--radio);
}

.marca { font-weight: 700; letter-spacing: .02em; }
.marca span { color: var(--acento); }

a { color: var(--texto); }

/* Quien prefiere menos movimiento no debería sufrir el punto pulsante del
   indicador de LIVE ni las transiciones de la ventana. */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: .001ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: .001ms !important;
  }
}
```

- [ ] **Step 5: Escribir `internal/web/router.go`**

```go
// Package web expone el livestream y las cuentas por HTTP.
//
// Es el único paquete que conoce a la vez el motor, el hub y la autenticación:
// hls no sabe que existe HTTP, y viewers no sabe que existe hls.
package web

import (
	"context"
	"embed"
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

// cacheDeAssets deja cachear los estáticos una hora.
//
// Una hora y no un año porque los nombres no llevan huella del contenido: con
// `immutable`, un cambio de CSS quedaría invisible para quien ya visitó la
// página. Los .ts sí usan `immutable`, porque su contenido nunca cambia.
func cacheDeAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
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
```

- [ ] **Step 6: Correr los tests y verificar que pasan**

Run: `go test ./internal/web/ -v -count=1`
Expected: PASS, 10 tests.

Dos de ellos existen porque su ausencia dejaba pasar mutaciones silenciosas:
borrar `codigo: http.StatusOK` de `respuestaObservada` (todas las peticiones que
sólo escriben quedarían registradas con un 0) e invertir el orden a
`recuperar(registrar(mux))` (la petición que tumba al handler sería la única sin
línea de log). Ninguna de las dos rompía nada antes.

- [ ] **Step 7: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add internal/web
git commit -m "feat(web): router, middleware y estáticos embebidos

Los assets viajan dentro del binario con go:embed: el contenedor no depende
del directorio de trabajo ni necesita copiarlos. El healthcheck consulta la
base en vez de devolver 200 siempre, o Docker reiniciaría contenedores sanos
y dejaría vivos los rotos. registrar envuelve a recuperar para que la línea
de log exista también cuando el handler entra en pánico, y el envoltorio
reexpone Flush porque el SSE lo necesita."
```

---

### Task 4: `internal/web/stream.go` — playlist, segmentos y el adaptador del hook

**Files:**
- Create: `internal/web/stream.go`
- Modify: `internal/web/router.go` (registrar dos rutas)
- Test: `internal/web/stream_test.go`

**Interfaces:**
- Consumes: `hls.Engine.Current()`, `hls.Pool.Resolve(name)`, `viewers.Hub.Publicar`, `Deps` y los helpers de test de la Task 3.
- Produces:
  - `func HookDeRotacion(h *viewers.Hub) func(*hls.Snapshot)` — lo usa `cmd/server` en la Task 8 para conectar el motor al hub.
  - Rutas `GET /live/stream.m3u8` y `GET /live/segments/{name}`, ambas bajo `RequireAPI`.

**Las dos rutas son hermanas y eso no es decorativo.** El `.m3u8` referencia sus segmentos como `segments/segmentN.ts`, en relativo. Servir el playlist desde cualquier otra ruta rompe esos enlaces y produce 404 silenciosos — el player simplemente no arranca y no dice por qué. El contrato está escrito en `internal/hls/snapshot.go` sobre la constante `segmentURIPrefix`.

**Por qué el `no-cache` del playlist no es opcional:** si el navegador o un proxy cachea el `.m3u8`, el player recibe una ventana vieja, pide segmentos que ya salieron de la ventana y la reproducción se corta. Los `.ts`, en cambio, son inmutables por definición y se cachean un año.

**Por qué el adaptador vive acá y no en `viewers`:** es el único punto que necesita conocer a la vez `hls` y `viewers`. Poniéndolo en `web`, el hub sigue sin importar `hls` y se prueba solo, y `hls` sigue sin conocer al hub.

- [ ] **Step 1: Escribir los tests que fallan**

Archivo `internal/web/stream_test.go`:

```go
package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/hls"
	"zapping-live/internal/viewers"
)

func TestPlaylistCabeceras(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("Content-Type = %q, quiero application/vnd.apple.mpegurl", ct)
	}
	// Sin no-store, un proxy entrega una ventana vieja, el player pide
	// segmentos que ya salieron de la ventana y la reproducción se corta.
	cc := w.Header().Get("Cache-Control")
	for _, quiero := range []string{"no-cache", "no-store"} {
		if !strings.Contains(cc, quiero) {
			t.Errorf("Cache-Control = %q, le falta %q", cc, quiero)
		}
	}
}

func TestPlaylistEsElDelSnapshot(t *testing.T) {
	// El .m3u8 se renderiza una vez por rotación dentro del snapshot; el
	// handler sólo escribe bytes ya listos. Si alguien lo re-renderizara por
	// request, este test lo seguiría pasando, pero el de abajo lo delata.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	quiero := b.Motor.Current().Playlist
	if !bytes.Equal(w.Body.Bytes(), quiero) {
		t.Fatalf("cuerpo = %q, quiero %q", w.Body.String(), quiero)
	}
	if !strings.Contains(w.Body.String(), "segments/segment0.ts") {
		t.Errorf("las URI deben ser relativas a segments/, el cuerpo es:\n%s", w.Body.String())
	}
}

func TestPlaylistNoMutaElSnapshotCompartido(t *testing.T) {
	// Snapshot.Playlist es de SÓLO LECTURA: lo comparten por referencia todos
	// los lectores concurrentes. Dos peticiones seguidas tienen que ver los
	// mismos bytes; si el handler escribiera sobre ellos, la segunda vería
	// basura.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)
	antes := append([]byte(nil), b.Motor.Current().Playlist...)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
		r.AddCookie(galleta)
		b.Handler.ServeHTTP(httptest.NewRecorder(), r)
	}

	if !bytes.Equal(antes, b.Motor.Current().Playlist) {
		t.Fatal("el handler mutó Snapshot.Playlist, que es compartido y de sólo lectura")
	}
}

func TestPlaylistSinSesionEs401(t *testing.T) {
	// 401 y NO 302: con un redirect, hls.js intentaría parsear la página de
	// login como playlist y reportaría un error incomprensible.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/live/stream.m3u8", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", w.Code)
	}
}

func TestSegmentoSirveElArchivo(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/segments/segment1.ts", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if got := w.Body.String(); got != "contenido-falso-del-segment1.ts" {
		t.Errorf("cuerpo = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("Content-Type = %q, quiero video/mp2t", ct)
	}
	// Los .ts no cambian nunca: cachearlos agresivamente le ahorra al servidor
	// toda la ventana ya vista.
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control = %q, quiero public, max-age=31536000, immutable", cc)
	}
}

func TestSegmentoSoportaRangos(t *testing.T) {
	// http.ServeContent da range requests sin código extra, y es lo que permite
	// que el player pida trozos en vez del archivo entero. También es la razón
	// de servir con ServeContent en vez de io.Copy.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/segments/segment0.ts", nil)
	r.AddCookie(galleta)
	r.Header.Set("Range", "bytes=0-8")
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("código = %d, quiero 206", w.Code)
	}
	if got := w.Body.String(); got != "contenido" {
		t.Errorf("cuerpo = %q, quiero \"contenido\"", got)
	}
}

func TestSegmentoTraversal(t *testing.T) {
	// Pool.Resolve es una lista blanca contra el índice del manifiesto: un
	// nombre que no está en el pool no resuelve, punto. Las variantes
	// codificadas existen porque el mux de Go des-escapa el comodín, así que
	// %2e%2e%2f llega al handler como "../" sin pasar por la limpieza de ruta.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	rutas := []string{
		"/live/segments/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/live/segments/%2e%2e%2fsegment.m3u8",
		"/live/segments/..%2fsegment.m3u8",
		"/live/segments/segment.m3u8",
		"/live/segments/segment99.ts",
	}
	for _, ruta := range rutas {
		t.Run(ruta, func(t *testing.T) {
			r := httptest.NewRequest("GET", ruta, nil)
			r.AddCookie(galleta)
			w := httptest.NewRecorder()
			b.Handler.ServeHTTP(w, r)

			if w.Code == http.StatusOK {
				t.Fatalf("código = 200: sirvió algo que no está en el pool, cuerpo = %q", w.Body.String())
			}
			if w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
				t.Errorf("código = %d, quiero 404 (o 301 si el mux limpió la ruta)", w.Code)
			}
		})
	}
}

func TestSegmentoSinSesionEs401(t *testing.T) {
	// Proteger sólo /player dejaría los .ts descargables sin cuenta, que es
	// exactamente lo que el requisito 4 busca impedir.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/live/segments/segment0.ts", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", w.Code)
	}
}

func TestHookDeRotacionArmaElEvento(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := viewers.NewHub()
	go h.Run(ctx)

	ch, salir := h.Suscribir()
	defer salir()
	for len(ch) > 0 {
		<-ch
	}

	snap := &hls.Snapshot{
		Seq:     142,
		HasDisc: true,
		Window: []hls.Segment{
			{Name: "segment14.ts", Duration: 10 * time.Second},
			{Name: "segment15.ts", Duration: 10 * time.Second},
			{Name: "segment16.ts", Duration: 10 * time.Second},
		},
		NextAt: time.Now().Add(4 * time.Second),
	}
	HookDeRotacion(h)(snap)

	select {
	case e := <-ch:
		if e.Secuencia != 142 {
			t.Errorf("Secuencia = %d, quiero 142", e.Secuencia)
		}
		if !e.Discontinuidad {
			t.Error("Discontinuidad = false, quiero true")
		}
		quiero := []string{"segment14.ts", "segment15.ts", "segment16.ts"}
		if len(e.Ventana) != len(quiero) {
			t.Fatalf("Ventana = %v, quiero %v", e.Ventana, quiero)
		}
		for i := range quiero {
			if e.Ventana[i] != quiero[i] {
				t.Errorf("Ventana[%d] = %q, quiero %q", i, e.Ventana[i], quiero[i])
			}
		}
		if e.ProximaEnMs <= 0 || e.ProximaEnMs > 4000 {
			t.Errorf("ProximaEnMs = %d, quiero algo en (0, 4000]", e.ProximaEnMs)
		}
	case <-time.After(time.Second):
		t.Fatal("el hub no recibió el evento del hook")
	}
}

func TestHookDeRotacionNoBloqueaConElHubDetenido(t *testing.T) {
	// LA restricción heredada del bloque 02: el hook corre síncronamente en la
	// goroutine que hace avanzar el stream. Si se bloqueara —por ejemplo
	// porque el hub todavía no arrancó o ya se apagó— el stream se detendría
	// para todos los espectadores. Sin el select/default de Publicar, este
	// test cuelga.
	h := viewers.NewHub() // Run nunca se llama
	hook := HookDeRotacion(h)

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < 200; i++ {
			hook(&hls.Snapshot{Seq: int64(i), NextAt: time.Now().Add(time.Second)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(2 * time.Second):
		t.Fatal("el hook se bloqueó: eso detendría el avance del stream")
	}
}

func TestHookDeRotacionNoMutaElSnapshot(t *testing.T) {
	// Snapshot.Window es de sólo lectura y se comparte entre todos los
	// lectores. El hook tiene que COPIAR los nombres, no quedarse con el slice.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := viewers.NewHub()
	go h.Run(ctx)

	ventana := []hls.Segment{{Name: "segment0.ts", Duration: 10 * time.Second}}
	snap := &hls.Snapshot{Seq: 1, Window: ventana, NextAt: time.Now().Add(time.Second)}
	HookDeRotacion(h)(snap)

	if snap.Window[0].Name != "segment0.ts" || len(snap.Window) != 1 {
		t.Fatalf("el hook modificó Snapshot.Window: %+v", snap.Window)
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/web/ -run 'Playlist|Segmento|Hook' -v`
Expected: FAIL — `HookDeRotacion` no existe y las rutas devuelven 404.

- [ ] **Step 3: Escribir `internal/web/stream.go`**

```go
package web

import (
	"log"
	"net/http"
	"os"
	"time"

	"zapping-live/internal/hls"
	"zapping-live/internal/viewers"
)

// manejadorStream sirve el playlist y los segmentos.
type manejadorStream struct {
	motor *hls.Engine
	pool  *hls.Pool
	log   *log.Logger
}

// playlist entrega el .m3u8 vigente.
//
// Todo el trabajo ya está hecho: el snapshot trae el playlist renderizado desde
// la rotación, así que esta petición es un Load atómico y un Write de bytes
// listos. Cero formateo y cero asignaciones por espectador.
func (s *manejadorStream) playlist(w http.ResponseWriter, r *http.Request) {
	snap := s.motor.Current() // wait-free: no toma locks ni compite con el motor

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Obligatorio: un playlist cacheado le entrega al player una ventana vieja,
	// que lo lleva a pedir segmentos ya expirados y a cortar la reproducción.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// snap.Playlist es de SÓLO LECTURA y compartido por referencia entre todos
	// los lectores: se escribe tal cual, nunca se modifica.
	w.Write(snap.Playlist)
}

// segmento entrega un .ts.
//
// El nombre se valida contra el pool, que es una lista blanca construida al
// parsear el manifiesto: un nombre que no está ahí no resuelve, y con eso el
// path traversal queda cerrado sin tener que sanear cadenas. Hace falta de
// verdad, porque el mux des-escapa el comodín: "%2e%2e%2f" llega acá como
// "../" sin pasar por la limpieza de ruta de net/http.
func (s *manejadorStream) segmento(w http.ResponseWriter, r *http.Request) {
	nombre := r.PathValue("name")
	ruta, ok := s.pool.Resolve(nombre)
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(ruta)
	if err != nil {
		// Un segmento que falta es un problema de despliegue, no del cliente:
		// se registra, pero no se tumba el stream por eso.
		s.log.Printf("web: abriendo el segmento %q: %v", nombre, err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.log.Printf("web: stat del segmento %q: %v", nombre, err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}

	// El contenido de un .ts nunca cambia, así que se cachea un año. Es lo que
	// hace que revisitar la ventana no vuelva a costarle disco al servidor.
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	// ServeContent copia por bloques desde el *os.File y resuelve los range
	// requests. Un segmento de 13 MB cuesta del orden de KB de RAM, no 13 MB:
	// es una de las tres decisiones de memoria del proyecto.
	http.ServeContent(w, r, nombre, info.ModTime(), f)
}

// HookDeRotacion adapta un snapshot del motor a un evento del hub.
//
// Vive en `web` porque es el único punto que necesita conocer a la vez `hls` y
// `viewers`: así el hub no importa `hls` y `hls` sigue sin saber que existe un
// hub.
//
// CORRE SÍNCRONAMENTE EN LA GOROUTINE DE ROTACIÓN DEL MOTOR. No puede bloquear
// ni entrar en pánico: lo primero detendría el avance del stream para todos los
// espectadores, lo segundo tumbaría la goroutine del motor. Hub.Publicar
// descarta en vez de bloquear, y acá no hay nada que pueda entrar en pánico.
func HookDeRotacion(h *viewers.Hub) func(*hls.Snapshot) {
	return func(s *hls.Snapshot) {
		// Se COPIAN los nombres: s.Window es de sólo lectura y compartido por
		// referencia con todos los lectores concurrentes. Es una asignación
		// cada ~10 segundos, no por petición.
		ventana := make([]string, len(s.Window))
		for i, seg := range s.Window {
			ventana[i] = seg.Name
		}

		h.Publicar(viewers.Evento{
			Secuencia:      s.Seq,
			Ventana:        ventana,
			ProximaEnMs:    time.Until(s.NextAt).Milliseconds(),
			Discontinuidad: s.HasDisc,
		})
	}
}
```

- [ ] **Step 4: Registrar las rutas en `internal/web/router.go`**

Dentro de `NewRouter`, justo después de la línea de los estáticos:

```go
	st := &manejadorStream{motor: d.Motor, pool: d.Pool, log: d.Log}

	// Rutas HERMANAS, y eso es un contrato, no un gusto: las URI del .m3u8 son
	// relativas ("segments/segmentN.ts"). Servir el playlist desde otra ruta
	// rompe esos enlaces y da 404 silenciosos.
	mux.Handle("GET /live/stream.m3u8", d.Guard.RequireAPI(http.HandlerFunc(st.playlist)))
	mux.Handle("GET /live/segments/{name}", d.Guard.RequireAPI(http.HandlerFunc(st.segmento)))
```

- [ ] **Step 5: Correr los tests y verificar que pasan**

Run: `go test ./internal/web/ -v -count=1`
Expected: PASS. `TestSegmentoTraversal` con 5 subtests.

- [ ] **Step 6: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add internal/web
git commit -m "feat(web): servir el playlist y los segmentos protegidos

El playlist sale del snapshot ya renderizado: la petición es un Load atómico
y un Write, sin formateo ni asignaciones por espectador. Los .ts van por
http.ServeContent, que copia por bloques —13 MB de segmento cuestan KB de RAM
del servidor— y resuelve range requests gratis. El nombre del segmento se
valida contra la lista blanca del pool, que hace falta de verdad porque el
mux des-escapa el comodín y %2e%2e%2f llega como ../ al handler.

El adaptador snapshot→evento vive acá y no en viewers para que el hub no
importe hls; copia los nombres de la ventana porque Snapshot.Window es de
sólo lectura y compartido."
```

---

### Task 5: `internal/web/pages.go` — las tres páginas del requisito 2

**Files:**
- Create: `internal/web/pages.go`, `internal/web/templates/base.html`, `internal/web/templates/register.html`, `internal/web/templates/login.html`, `internal/web/templates/player.html`
- Modify: `internal/web/router.go` (registrar seis rutas)
- Test: `internal/web/pages_test.go`

**Interfaces:**
- Consumes: `cuenta.Registrar`, `cuenta.Store.PorEmail`, `cuenta.ErrorValidacion`, `cuenta.ErrEmailEnUso`, `cuenta.ErrNoEncontrado`, `auth.HashPassword`, `auth.VerifyPassword`, `auth.VerificarEnVacio`, `auth.UsuarioDe`, `Sessions.Crear/Destruir/DestruirDeUsuario`, `Guard.PonerCookie/BorrarCookie/RequirePage`.
- Produces: rutas `GET /{$}`, `GET|POST /register`, `GET|POST /login`, `POST /logout`, `GET /player`. La Task 7 completa `player.html` con el panel y los scripts.

**Cuatro restricciones heredadas se cumplen acá. Ninguna es opcional:**

1. **El alta va por `cuenta.Registrar`**, nunca por `Store.Crear`. `Crear` recibe el hash ya hecho y no valida: llamarlo desde el handler permitiría guardar la contraseña en claro.
2. **`auth.VerificarEnVacio()` cuando el email no existe.** Sin esa llamada, un email inexistente responde en microsegundos y uno registrado paga ~370 ms de bcrypt: el tiempo revela qué cuentas existen aunque el mensaje sea idéntico. La función ya está implementada y probada, y **este handler es su primer llamante**.
3. **`Sessions.DestruirDeUsuario` antes de `Sessions.Crear`** al iniciar sesión, contra session fixation.
4. **`Guard.PonerCookie(w, token)`**, que toma el TTL de `Sessions`. Nunca pasar el TTL por separado.

**Por qué cada página se parsea en su propio conjunto:** `html/template` guarda las plantillas por nombre en un único espacio. Si `login.html` y `register.html` definieran ambas `{{define "contenido"}}`, la segunda pisaría a la primera en silencio y una de las dos páginas mostraría el formulario equivocado. Un mapa de conjuntos, uno por página, hace que el problema no pueda existir.

**Por qué se renderiza a un buffer antes de escribir:** si la plantilla falla a mitad de camino, escribiendo directo sobre el `ResponseWriter` ya se habrían mandado el 200 y media página, y el usuario vería HTML cortado sin error. Con el buffer, o sale la página completa o sale un 500 limpio.

**Por qué 422 y no 200 al re-renderizar un formulario con error:** el 200 diría que la petición se procesó bien, y no es cierto. 422 es exacto y los navegadores renderizan el cuerpo igual.

**Por qué el escapado no necesita trabajo extra:** `html/template` escapa según el contexto (HTML, atributo, JS, URL). Un nombre de usuario `<script>alert(1)</script>` sale escapado sin que el handler haga nada. Es la razón de usar `html/template` y no `text/template`.

- [ ] **Step 1: Escribir los tests que fallan**

Archivo `internal/web/pages_test.go`:

```go
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
		t.Errorf("el input de la contraseña no quedó marcado:
%s", cuerpo)
	}
	if strings.Contains(cuerpo, `id="email" name="email" type="email" class="malo"`) {
		t.Error("se marcó el input del email, que era válido")
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/web/ -run 'Raiz|Formularios|Registro|Login|Logout|Player|Validacion' -v`
Expected: FAIL — las rutas no existen.

- [ ] **Step 3: Escribir los templates**

`internal/web/templates/base.html`:

```html
{{define "base"}}<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{block "titulo" .}}Zapping Live{{end}}</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
{{block "contenido" .}}{{end}}
{{block "scripts" .}}{{end}}
</body>
</html>{{end}}
```

`internal/web/templates/register.html`:

```html
{{define "titulo"}}Crear cuenta · Zapping Live{{end}}

{{define "contenido"}}
<main class="acceso">
  <form class="tarjeta vidrio" method="post" action="/register">
    <h1 class="marca">zapping<span>·</span>live</h1>
    <p class="sub">Creá tu cuenta para ver la transmisión.</p>

    {{if .Error}}<p class="aviso" role="alert">{{.Error}}</p>{{end}}

    <label for="nombre">Nombre</label>
    <input id="nombre" name="nombre" type="text" class="{{if eq .Campo "nombre"}}malo{{end}}" value="{{.Nombre}}" autocomplete="name" required autofocus>

    <label for="email">Email</label>
    <input id="email" name="email" type="email" class="{{if eq .Campo "email"}}malo{{end}}" value="{{.Email}}" autocomplete="email" required>

    <label for="contrasena">Contraseña</label>
    <input id="contrasena" name="contrasena" type="password" class="{{if eq .Campo "contraseña"}}malo{{end}}" autocomplete="new-password" minlength="8" required>
    <p class="pista">Mínimo 8 caracteres.</p>

    <button type="submit">Crear cuenta</button>
    <p class="pie">¿Ya tenés cuenta? <a href="/login">Iniciar sesión</a></p>
  </form>
</main>
{{end}}
```

`internal/web/templates/login.html`:

```html
{{define "titulo"}}Iniciar sesión · Zapping Live{{end}}

{{define "contenido"}}
<main class="acceso">
  <form class="tarjeta vidrio" method="post" action="/login">
    <h1 class="marca">zapping<span>·</span>live</h1>
    <p class="sub">Entrá para ver la transmisión en vivo.</p>

    {{if .Error}}<p class="aviso" role="alert">{{.Error}}</p>{{end}}

    <label for="email">Email</label>
    <input id="email" name="email" type="email" value="{{.Email}}" autocomplete="email" required autofocus>

    <label for="contrasena">Contraseña</label>
    <input id="contrasena" name="contrasena" type="password" autocomplete="current-password" required>

    <button type="submit">Entrar</button>
    <p class="pie">¿No tenés cuenta? <a href="/register">Crear una</a></p>
  </form>
</main>
{{end}}
```

`internal/web/templates/player.html` — versión mínima; la Task 7 le agrega el panel y los scripts:

```html
{{define "titulo"}}En vivo · Zapping Live{{end}}

{{define "contenido"}}
<header class="barra">
  <h1 class="marca">zapping<span>·</span>live</h1>
  <div class="sesion">
    <span class="quien">{{.Usuario.Name}}</span>
    <form method="post" action="/logout"><button type="submit" class="enlace">Cerrar sesión</button></form>
  </div>
</header>

<!-- La URL del playlist vive en el HTML, no en JavaScript: el servidor es
     quien conoce su arbol de rutas. La Task 7 la lee desde aca. -->
<main class="escenario" data-playlist="/live/stream.m3u8">
  <div class="marco">
    <video id="video" playsinline controls autoplay muted></video>
  </div>
</main>
{{end}}
```

- [ ] **Step 4: Escribir `internal/web/pages.go`**

```go
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
		p.render(w, "login", http.StatusUnauthorized,
			datosFormulario{Email: email, Error: mensajeCredenciales})
		return
	}

	if !auth.VerifyPassword(hash, clave) {
		p.render(w, "login", http.StatusUnauthorized,
			datosFormulario{Email: email, Error: mensajeCredenciales})
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
```

- [ ] **Step 5: Registrar las rutas en `internal/web/router.go`**

Dentro de `NewRouter`, antes del bloque de `/live/`:

```go
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
```

- [ ] **Step 6: Correr los tests y verificar que pasan**

Run: `go test ./internal/web/ -v -count=1`
Expected: PASS. La suite completa del paquete no debería pasar de ~2 s: sólo `TestRegistroCreaLaCuentaYDejaSesionIniciada` y `TestLoginConEmailInexistentePagaBcrypt` pagan bcrypt con costo 12, una vez cada uno.

- [ ] **Step 7: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add internal/web
git commit -m "feat(web): registro, login, logout y página del player

Cierra las cuatro restricciones que el bloque 03 dejó sin llamante:
el alta pasa por cuenta.Registrar (Store.Crear permitiría guardar la
contraseña en claro), el login llama a auth.VerificarEnVacio() cuando el
email no existe (si no, el tiempo de respuesta delata qué cuentas existen),
rota la sesión con DestruirDeUsuario contra session fixation, y la cookie
sale de Guard.PonerCookie para que TTL y fila caduquen juntos.

Cada página se parsea en su propio conjunto de plantillas: en uno solo, dos
{{define \"contenido\"}} se pisarían en silencio. El render va a un buffer
antes de tocar el ResponseWriter, para que un fallo a mitad de plantilla no
deje al usuario con un 200 y media página."
```

---

### Task 6: `internal/web/events.go` — el endpoint SSE

**Files:**
- Create: `internal/web/events.go`
- Modify: `internal/web/router.go` (registrar una ruta)
- Test: `internal/web/events_test.go`

**Interfaces:**
- Consumes: `viewers.Hub.Suscribir()`, `Deps.Hub`.
- Produces: ruta `GET /live/events`, protegida con `RequireAPI`.

**Por qué SSE y no WebSocket:** el flujo es de una sola dirección — el servidor cuenta, el cliente escucha. SSE es HTTP normal (funciona con cualquier proxy sin configuración especial), reconecta solo en el navegador, y `EventSource` son tres líneas de JavaScript. Un WebSocket traería una dependencia externa y un protocolo bidireccional que nadie necesita.

**Por qué el latido:** entre rotaciones pueden pasar 10 segundos sin un solo byte. Un proxy inverso con timeout de inactividad corta la conexión y el panel se queda congelado hasta que `EventSource` reconecte. Un comentario SSE (`: ping`) cada 20 segundos lo evita y el navegador lo descarta solo.

**Por qué se prueba contra un servidor real y no con `httptest.NewRecorder`:** el `Recorder` acumula todo en memoria y no devuelve nada hasta que el handler termina — y este handler no termina nunca. Con `httptest.NewServer` se lee el cuerpo a medida que llega, que es exactamente el comportamiento que hay que verificar.

- [ ] **Step 1: Escribir los tests que fallan**

Archivo `internal/web/events_test.go`:

```go
package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/viewers"
)

// abrirSSE conecta al endpoint y devuelve un lector de líneas.
func abrirSSE(t *testing.T, srv *httptest.Server, galleta *http.Cookie) (*http.Response, *bufio.Reader) {
	t.Helper()
	r, err := http.NewRequest("GET", srv.URL+"/live/events", nil)
	if err != nil {
		t.Fatalf("armando la petición: %v", err)
	}
	if galleta != nil {
		r.AddCookie(galleta)
	}
	// El timeout cubre TODA la lectura del cuerpo, no sólo la conexión. Sin él,
	// un handler que dejara de mandar eventos colgaría el test para siempre en
	// vez de fallar; con él, la lectura devuelve error y el test lo reporta.
	cliente := srv.Client()
	cliente.Timeout = 5 * time.Second
	resp, err := cliente.Do(r)
	if err != nil {
		t.Fatalf("conectando al SSE: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body)
}

// leerEvento devuelve el primer bloque "data:" que llegue, saltando latidos.
func leerEvento(t *testing.T, br *bufio.Reader) viewers.Evento {
	t.Helper()
	plazo := time.AfterFunc(2*time.Second, func() {
		t.Error("se agotó el plazo esperando un evento del SSE")
	})
	defer plazo.Stop()

	for {
		linea, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("leyendo del SSE: %v", err)
		}
		if !strings.HasPrefix(linea, "data: ") {
			continue // comentario de latido o línea en blanco
		}
		var e viewers.Evento
		if err := json.Unmarshal([]byte(strings.TrimPrefix(linea, "data: ")), &e); err != nil {
			t.Fatalf("el evento no es JSON válido (%q): %v", linea, err)
		}
		return e
	}
}

func TestSSECabecerasYPrimerEvento(t *testing.T) {
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()
	_, galleta := usuarioConSesion(t, b)

	resp, br := abrirSSE(t, srv, galleta)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, quiero text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, quiero no-cache", cc)
	}

	// El hub entrega el estado vigente al conectar: sin eso el panel quedaría
	// vacío hasta la próxima rotación, que puede tardar 10 segundos.
	e := leerEvento(t, br)
	if e.Espectadores != 1 {
		t.Errorf("Espectadores = %d, quiero 1", e.Espectadores)
	}
}

func TestSSERecibeLasRotaciones(t *testing.T) {
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()
	_, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)
	leerEvento(t, br) // el estado inicial

	// Simula una rotación del motor por la misma vía que usa cmd/server.
	b.Hub.Publicar(viewers.Evento{
		Secuencia:      77,
		Ventana:        []string{"segment7.ts", "segment8.ts", "segment9.ts"},
		ProximaEnMs:    4200,
		Discontinuidad: true,
	})

	e := leerEvento(t, br)
	if e.Secuencia != 77 {
		t.Errorf("Secuencia = %d, quiero 77", e.Secuencia)
	}
	if len(e.Ventana) != 3 || e.Ventana[0] != "segment7.ts" {
		t.Errorf("Ventana = %v", e.Ventana)
	}
	if e.ProximaEnMs != 4200 {
		t.Errorf("ProximaEnMs = %d, quiero 4200", e.ProximaEnMs)
	}
	if !e.Discontinuidad {
		t.Error("Discontinuidad = false, quiero true")
	}
}

func TestSSECuentaDosPestanas(t *testing.T) {
	// El criterio de aceptación textual del bloque: abrir dos pestañas sube el
	// contador a 2, cerrar una lo baja a 1.
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()
	_, galleta := usuarioConSesion(t, b)

	_, br1 := abrirSSE(t, srv, galleta)
	if e := leerEvento(t, br1); e.Espectadores != 1 {
		t.Fatalf("con una pestaña: Espectadores = %d, quiero 1", e.Espectadores)
	}

	resp2, br2 := abrirSSE(t, srv, galleta)
	if e := leerEvento(t, br2); e.Espectadores != 2 {
		t.Fatalf("con dos pestañas: Espectadores = %d, quiero 2", e.Espectadores)
	}
	// La primera pestaña también se entera, sin recargar nada.
	if e := leerEvento(t, br1); e.Espectadores != 2 {
		t.Errorf("la primera pestaña vio Espectadores = %d, quiero 2", e.Espectadores)
	}

	resp2.Body.Close()
	if e := leerEvento(t, br1); e.Espectadores != 1 {
		t.Errorf("tras cerrar una pestaña: Espectadores = %d, quiero 1", e.Espectadores)
	}
}

func TestSSEDesconexionDesRegistra(t *testing.T) {
	// r.Context().Done() es lo que garantiza que cerrar la pestaña no deje una
	// goroutine colgada esperando eventos para nadie.
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()
	_, galleta := usuarioConSesion(t, b)

	resp, br := abrirSSE(t, srv, galleta)
	leerEvento(t, br)
	if got := b.Hub.Espectadores(); got != 1 {
		t.Fatalf("Espectadores() = %d, quiero 1", got)
	}

	resp.Body.Close()

	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		if b.Hub.Espectadores() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Espectadores() = %d tras cerrar: el cliente no se dio de baja", b.Hub.Espectadores())
}

func TestSSESinSesionEs401(t *testing.T) {
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/live/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", resp.StatusCode)
	}
}

func TestSSENoFiltraDatosDelUsuario(t *testing.T) {
	// El evento se difunde a TODOS los espectadores. Si alguna vez alguien
	// agregara el nombre o el email al Evento, todos verían los de todos.
	b := entorno(t)
	srv := httptest.NewServer(b.Handler)
	defer srv.Close()
	u, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)
	linea, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("leyendo del SSE: %v", err)
	}
	for _, secreto := range []string{u.Email, u.Name} {
		if strings.Contains(linea, secreto) {
			t.Errorf("el evento difundido incluye %q, que es de un usuario concreto", secreto)
		}
	}
}
```

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./internal/web/ -run SSE -v`
Expected: FAIL — la ruta `/live/events` devuelve 404.

- [ ] **Step 3: Escribir `internal/web/events.go`**

```go
package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"zapping-live/internal/viewers"
)

// intervaloLatido mantiene viva la conexión entre rotaciones.
//
// Pueden pasar 10 segundos sin un solo byte, y un proxy inverso con timeout de
// inactividad corta ahí. El comentario SSE que se manda cada 20 s no llega a
// EventSource —el navegador descarta los comentarios— pero sí cuenta como
// tráfico para el proxy.
const intervaloLatido = 20 * time.Second

type manejadorEventos struct {
	hub *viewers.Hub
	log *log.Logger
}

// sse mantiene abierta una conexión de eventos hacia un espectador.
func (m *manejadorEventos) sse(w http.ResponseWriter, r *http.Request) {
	vaciar, ok := w.(http.Flusher)
	if !ok {
		// Inalcanzable con net/http, pero un envoltorio mal escrito lo haría
		// posible: sin Flush los eventos se quedarían en el buffer y el panel
		// no se movería nunca. Mejor fallar fuerte que quedarse mudo.
		m.log.Print("web: el ResponseWriter no implementa http.Flusher; el SSE no puede funcionar")
		http.Error(w, "streaming no disponible", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx bufferiza las respuestas por defecto y eso convierte un stream de
	// eventos en una entrega a bloques: el panel se movería a saltos o no se
	// movería nunca.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	vaciar.Flush() // que el cliente sepa que está conectado antes del primer evento

	// Suscribir devuelve un canal ya con el estado vigente adentro, así que el
	// panel se pinta al instante en vez de esperar a la próxima rotación.
	eventos, salir := m.hub.Suscribir()
	defer salir()

	latido := time.NewTicker(intervaloLatido)
	defer latido.Stop()

	for {
		select {
		case e, abierto := <-eventos:
			if !abierto {
				return // el hub se apagó: se cierra la conexión ordenadamente
			}
			cuerpo, err := json.Marshal(e)
			if err != nil {
				// viewers.Evento es JSON-serializable por construcción; si
				// esto ocurriera, sería un cambio de tipo mal hecho.
				m.log.Printf("web: serializando el evento: %v", err)
				continue
			}
			// El formato de SSE: "data: <carga>" y una línea en blanco que
			// cierra el mensaje. Sin esa línea, el navegador espera más y no
			// entrega nada.
			if _, err := w.Write(append(append([]byte("data: "), cuerpo...), '\n', '\n')); err != nil {
				return // el espectador se fue a mitad de escritura
			}
			vaciar.Flush()

		case <-latido.C:
			if _, err := w.Write([]byte(": latido\n\n")); err != nil {
				return
			}
			vaciar.Flush()

		case <-r.Context().Done():
			// El espectador cerró la pestaña. Esto es lo que garantiza que no
			// queden goroutines huérfanas: el defer de arriba lo da de baja.
			return
		}
	}
}
```

- [ ] **Step 4: Registrar la ruta en `internal/web/router.go`**

Junto a las otras rutas de `/live/`:

```go
	ev := &manejadorEventos{hub: d.Hub, log: d.Log}
	mux.Handle("GET /live/events", d.Guard.RequireAPI(http.HandlerFunc(ev.sse)))
```

- [ ] **Step 5: Correr los tests y verificar que pasan**

Run: `go test ./internal/web/ -v -count=1`
Expected: PASS, toda la suite del paquete.

- [ ] **Step 6: Verificar que no hay carreras**

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "C:/Users/Matias/Desktop/PruebaTecnica:/src" -w /src \
  golang:1.26 go test -race -count=1 ./...
```
Expected: todo `ok`, sin advertencias de carrera. Si Docker no está disponible, anotarlo y dejarlo para la Task 9.

- [ ] **Step 7: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add internal/web
git commit -m "feat(web): endpoint SSE del panel en vivo

SSE y no WebSocket porque el flujo es de una sola dirección: es HTTP normal,
atraviesa cualquier proxy y reconecta solo en el navegador. El latido cada
20 s existe porque entre rotaciones pueden pasar 10 segundos sin un byte y un
proxy con timeout de inactividad cortaría la conexión.

r.Context().Done() da de baja al espectador que cierra la pestaña, que es lo
que impide que queden goroutines esperando eventos para nadie. Se prueba
contra un httptest.NewServer real: el Recorder no devuelve nada hasta que el
handler termina, y este no termina nunca."
```

---

### Task 7: Frontend — hls.js vendorizado, panel en vivo y glassmorfismo

**Files:**
- Create: `internal/web/static/vendor/hls.min.js`, `internal/web/static/vendor/hls.LICENSE.txt`, `internal/web/static/player.js`
- Modify: `internal/web/static/app.css` (componentes), `internal/web/templates/player.html` (panel y scripts)
- Test: `internal/web/frontend_test.go`

**Interfaces:**
- Consumes: `GET /live/stream.m3u8`, `GET /live/events`, `POST /logout`, y los estáticos embebidos de la Task 3.
- Produces: la página del player completa. Nada de Go nuevo.

**hls.js vendorizado, no por CDN.** El enunciado pide "un docker con el aplicativo **funcionando**". Si el evaluador levanta el contenedor sin internet —o simplemente detrás de un firewall corporativo— un `<script src="https://cdn...">` deja la página con un `<video>` negro y un error en consola. El archivo va commiteado con su licencia y su versión anotada.

**Regla estricta: `backdrop-filter` nunca encima del `<video>`.** El desenfoque se recalcula cada vez que cambia lo que hay detrás; sobre un video en reproducción son 25-30 recálculos en GPU por segundo, y en una máquina modesta eso puede tirar frames del propio video. Los paneles de vidrio van **al lado**. El único overlay que se superpone (el botón "volver al vivo") usa un degradado semitransparente sin `backdrop-filter`: se ve casi igual y cuesta ~0. **La Task 7 incluye un test que verifica esto sobre el CSS**, para que no se pierda en un retoque futuro.

**Por qué la cuenta regresiva se anima en el cliente:** pedirle al servidor la fracción de segundo que falta sería una petición por frame. El SSE manda `nextRotationMs` una vez por rotación y `requestAnimationFrame` interpola entre eventos; el servidor sólo corrige la referencia. Es el mismo principio que el motor: derivar del reloj en vez de contar.

**El panel no es adorno.** Le muestra al evaluador, en tiempo real, la mecánica que está calificando: el `MEDIA-SEQUENCE` subiendo, los segmentos desplazándose, y la rotación corta cuando le toca al segmento de 4,57 s.

- [ ] **Step 1: Descargar hls.js y su licencia**

```bash
mkdir -p internal/web/static/vendor
curl -sSL -o internal/web/static/vendor/hls.min.js       https://cdn.jsdelivr.net/npm/hls.js@1.6.16/dist/hls.min.js
curl -sSL -o internal/web/static/vendor/hls.LICENSE.txt  https://raw.githubusercontent.com/video-dev/hls.js/v1.6.16/LICENSE
ls -l internal/web/static/vendor/
```
Expected: `hls.min.js` de varios cientos de KB y la licencia Apache-2.0. Si `hls.min.js` pesa menos de 100 KB, la descarga falló (un 404 de jsdelivr devuelve una página) — verificar antes de seguir.

- [ ] **Step 2: Escribir los tests que fallan**

Archivo `internal/web/frontend_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHlsSeSirveVendorizado(t *testing.T) {
	// Si esto falla, el player no arranca en un contenedor sin internet, que
	// es exactamente el escenario en que lo va a levantar el evaluador.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/vendor/hls.min.js", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if n := w.Body.Len(); n < 100_000 {
		t.Fatalf("hls.min.js pesa %d bytes: la descarga de la Task 7 falló", n)
	}
}

func TestElFrontendNoPideNadaAInternet(t *testing.T) {
	// El criterio de aceptación "funciona con el contenedor aislado de
	// internet", verificado sobre el código en vez de a mano. Se revisan los
	// archivos que escribimos nosotros; hls.min.js queda fuera porque su
	// minificado contiene URLs dentro de literales de texto.
	nuestros := []string{
		"templates/base.html",
		"templates/login.html",
		"templates/register.html",
		"templates/player.html",
		"static/app.css",
		"static/player.js",
	}
	externo := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)

	for _, nombre := range nuestros {
		t.Run(nombre, func(t *testing.T) {
			var datos []byte
			var err error
			if strings.HasPrefix(nombre, "templates/") {
				datos, err = archivosPlantillas.ReadFile(nombre)
			} else {
				datos, err = archivosEstaticos.ReadFile(nombre)
			}
			if err != nil {
				t.Fatalf("leyendo %s: %v", nombre, err)
			}
			for _, linea := range strings.Split(string(datos), "\n") {
				// Las URL en comentarios CSS/JS no generan peticiones.
				recortada := strings.TrimSpace(linea)
				if strings.HasPrefix(recortada, "/*") || strings.HasPrefix(recortada, "*") || strings.HasPrefix(recortada, "//") {
					continue
				}
				if m := externo.FindString(linea); m != "" {
					t.Errorf("referencia externa %q en: %s", m, recortada)
				}
			}
		})
	}
}

func TestNingunVidrioSeSuperponeAlVideo(t *testing.T) {
	// La regla estricta del proyecto, verificada sobre el CSS y no confiada a
	// la memoria de quien lo retoque: backdrop-filter recalcula el desenfoque
	// cada vez que cambia lo que hay detrás. Sobre un video en reproducción son
	// 25-30 recálculos en GPU por segundo y puede tirar frames del video.
	datos, err := archivosEstaticos.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("leyendo app.css: %v", err)
	}
	css := string(datos)

	// Selectores que están sobre el <video> o lo contienen.
	prohibidos := []string{"video", ".marco", ".superpuesto", ".sobre-video"}

	bloque := regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	for _, m := range bloque.FindAllStringSubmatch(css, -1) {
		selector, cuerpo := strings.TrimSpace(m[1]), m[2]
		if !strings.Contains(cuerpo, "backdrop-filter") {
			continue
		}
		for _, malo := range prohibidos {
			for _, parte := range strings.Split(selector, ",") {
				parte = strings.TrimSpace(parte)
				if parte == malo || strings.HasPrefix(parte, malo+" ") ||
					strings.HasPrefix(parte, malo+":") || strings.HasSuffix(parte, " "+malo) {
					t.Errorf("el selector %q usa backdrop-filter sobre el video: está prohibido", selector)
				}
			}
		}
	}
}

func TestPlayerCargaSusScripts(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)
	r := httptest.NewRequest("GET", "/player", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	cuerpo := w.Body.String()
	for _, quiero := range []string{
		"/static/vendor/hls.min.js",
		"/static/player.js",
		"/live/stream.m3u8",
		"/live/events",
		`id="video"`,
		`id="espectadores"`,
		`id="secuencia"`,
		`id="ventana"`,
	} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("la página del player no contiene %q", quiero)
		}
	}
}
```

- [ ] **Step 3: Correr los tests y verificar que fallan**

Run: `go test ./internal/web/ -run 'Hls|Frontend|Vidrio|PlayerCarga' -v`
Expected: FAIL — `player.js` no existe y `player.html` todavía no trae el panel.

- [ ] **Step 4: Completar `internal/web/templates/player.html`**

Reemplazar el archivo entero:

```html
{{define "titulo"}}En vivo · Zapping Live{{end}}

{{define "contenido"}}
<header class="barra">
  <h1 class="marca">zapping<span>·</span>live</h1>
  <div class="sesion">
    <span class="quien">{{.Usuario.Name}}</span>
    <form method="post" action="/logout"><button type="submit" class="enlace">Cerrar sesión</button></form>
  </div>
</header>

<!-- Las dos URL del stream viven acá y no en player.js: el servidor es quien
     conoce su propio árbol de rutas, y así hay una sola fuente de verdad en
     vez de una constante en Go y otra en JavaScript que pueden divergir. -->
<main class="escenario" data-playlist="/live/stream.m3u8" data-eventos="/live/events">
  <section class="marco">
    <video id="video" playsinline controls autoplay muted poster=""></video>
    <!-- Overlay SIN backdrop-filter: encima del video sólo va un degradado.
         Ver docs/06-decisiones.md. -->
    <button id="volver-al-vivo" class="superpuesto" hidden>▶ Volver al vivo</button>
    <p id="fallo" class="superpuesto fallo" hidden role="alert"></p>
  </section>

  <!-- Los paneles de vidrio van AL LADO del video, nunca encima. -->
  <aside class="panel vidrio">
    <p class="vivo"><span class="punto" aria-hidden="true"></span> EN VIVO</p>

    <p class="metrica">
      <span class="ojo" aria-hidden="true">👁</span>
      <span id="espectadores">—</span>
      <span class="unidad">espectadores</span>
    </p>

    <dl class="datos">
      <dt>media-sequence</dt>
      <dd id="secuencia">—</dd>
    </dl>

    <p class="etiqueta">ventana</p>
    <ol id="ventana" class="ventana" aria-live="polite"></ol>

    <p class="etiqueta">próxima rotación</p>
    <p class="cuenta"><span id="cuenta-regresiva">—</span></p>
    <div class="barra-progreso"><div id="progreso"></div></div>

    <p id="discontinuidad" class="disc" hidden>vuelta del ciclo · EXT-X-DISCONTINUITY</p>
  </aside>
</main>
{{end}}

{{define "scripts"}}
<script src="/static/vendor/hls.min.js"></script>
<script src="/static/player.js" defer></script>
{{end}}
```

- [ ] **Step 5: Agregar los componentes a `internal/web/static/app.css`**

Anexar al final del archivo:

```css
/* ---------- páginas de acceso (login y registro) ---------- */

.acceso {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 2rem 1rem;
}

.tarjeta {
  width: min(26rem, 100%);
  padding: 2rem;
  display: flex;
  flex-direction: column;
  gap: .4rem;
}

.tarjeta h1 { margin: 0; font-size: 1.6rem; }
.sub   { margin: 0 0 1rem; color: var(--texto-suave); font-size: .95rem; }
.pista { margin: 0; color: var(--texto-suave); font-size: .82rem; }
.pie   { margin: 1rem 0 0; color: var(--texto-suave); font-size: .9rem; text-align: center; }

label { margin-top: .7rem; font-size: .85rem; color: var(--texto-suave); }

input {
  padding: .7rem .85rem;
  border-radius: 10px;
  border: 1px solid var(--glass-borde);
  background: rgba(0, 0, 0, .25);
  color: var(--texto);
  font-size: 1rem;
}
input:focus-visible {
  outline: 2px solid var(--acento);
  outline-offset: 1px;
}

/* El campo que falló la validación. cuenta.ErrorValidacion trae cuál es
   justamente para poder señalarlo en vez de dejar al usuario buscándolo. */
input.malo {
  border-color: var(--error);
  background: rgba(255, 138, 138, .08);
}

button {
  margin-top: 1.2rem;
  padding: .8rem;
  border: 0;
  border-radius: 10px;
  background: var(--acento);
  color: #fff;
  font-size: 1rem;
  font-weight: 600;
  cursor: pointer;
}
button:hover { filter: brightness(1.1); }

.aviso {
  margin: .6rem 0 0;
  padding: .6rem .8rem;
  border-radius: 10px;
  border: 1px solid rgba(255, 138, 138, .35);
  background: rgba(255, 138, 138, .12);
  color: var(--error);
  font-size: .9rem;
}

/* ---------- player ---------- */

.barra {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 1rem 1.5rem;
}
.barra h1 { margin: 0; font-size: 1.2rem; }

.sesion { display: flex; align-items: center; gap: 1rem; }
.quien  { color: var(--texto-suave); font-size: .9rem; }

.enlace {
  margin: 0;
  padding: .4rem .8rem;
  background: transparent;
  border: 1px solid var(--glass-borde);
  color: var(--texto-suave);
  font-size: .85rem;
  font-weight: 400;
}

.escenario {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 17rem;
  gap: 1.25rem;
  align-items: start;
  padding: 0 1.5rem 2rem;
}

/* .marco contiene al <video>: NUNCA lleva backdrop-filter. */
.marco {
  position: relative;
  border-radius: var(--radio);
  overflow: hidden;
  background: #000;
  border: 1px solid var(--glass-borde);
}
.marco video { display: block; width: 100%; aspect-ratio: 16 / 9; background: #000; }

/* Overlays sobre el video: degradado semitransparente y punto. Sin
   backdrop-filter, que a 25-30 fps puede tirar frames del propio video. */
.superpuesto {
  position: absolute;
  left: 50%;
  transform: translateX(-50%);
  bottom: 4.5rem;
  margin: 0;
  padding: .55rem 1rem;
  border-radius: 999px;
  border: 1px solid rgba(255, 255, 255, .2);
  background: linear-gradient(rgba(15, 17, 22, .82), rgba(15, 17, 22, .92));
  color: var(--texto);
  font-size: .88rem;
  font-weight: 500;
  cursor: pointer;
}
.superpuesto.fallo { cursor: default; border-color: rgba(255, 138, 138, .4); color: var(--error); }

.panel { padding: 1.25rem; }

.vivo {
  margin: 0 0 1rem;
  display: flex;
  align-items: center;
  gap: .5rem;
  font-size: .8rem;
  font-weight: 700;
  letter-spacing: .12em;
}
.punto {
  width: .55rem; height: .55rem;
  border-radius: 50%;
  background: var(--acento);
  box-shadow: 0 0 0 0 rgba(229, 9, 20, .6);
  animation: latir 1.8s ease-out infinite;
}
@keyframes latir {
  70%  { box-shadow: 0 0 0 .55rem rgba(229, 9, 20, 0); }
  100% { box-shadow: 0 0 0 0 rgba(229, 9, 20, 0); }
}

.metrica { margin: 0 0 1.2rem; display: flex; align-items: baseline; gap: .45rem; }
.metrica #espectadores { font-size: 2rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.unidad { color: var(--texto-suave); font-size: .85rem; }

.datos { margin: 0 0 1.2rem; }
.datos dt { color: var(--texto-suave); font-size: .72rem; letter-spacing: .1em; text-transform: uppercase; }
.datos dd { margin: .1rem 0 0; font-size: 1.35rem; font-weight: 600; font-variant-numeric: tabular-nums; }

.etiqueta {
  margin: 0 0 .4rem;
  color: var(--texto-suave);
  font-size: .72rem;
  letter-spacing: .1em;
  text-transform: uppercase;
}

.ventana {
  list-style: none;
  display: flex;
  gap: .4rem;
  margin: 0 0 1.2rem;
  padding: 0;
}
.ventana li {
  flex: 1;
  padding: .5rem 0;
  border-radius: 10px;
  border: 1px solid var(--glass-borde);
  background: rgba(255, 255, 255, .05);
  text-align: center;
  font-size: .9rem;
  font-variant-numeric: tabular-nums;
  transition: transform .35s ease, background .35s ease;
}
.ventana li.entrando {
  transform: translateX(.5rem);
  background: rgba(229, 9, 20, .22);
}

.cuenta { margin: 0 0 .5rem; font-size: 1.35rem; font-weight: 600; font-variant-numeric: tabular-nums; }

.barra-progreso {
  height: 4px;
  border-radius: 999px;
  background: rgba(255, 255, 255, .1);
  overflow: hidden;
}
.barra-progreso div { height: 100%; width: 0; background: var(--acento); }

.disc {
  margin: 1rem 0 0;
  color: var(--texto-suave);
  font-size: .75rem;
  letter-spacing: .04em;
}

@media (max-width: 60rem) {
  .escenario { grid-template-columns: minmax(0, 1fr); }
}
```

- [ ] **Step 6: Escribir `internal/web/static/player.js`**

```js
// Player del livestream y panel de estado.
//
// Dos fuentes de datos independientes: hls.js consume el .m3u8 por HTTP, y un
// EventSource recibe el estado del stream por SSE. No se hablan entre sí a
// propósito — si el panel fallara, el video seguiría reproduciéndose.
(function () {
  'use strict';

  var video = document.getElementById('video');
  var fallo = document.getElementById('fallo');
  var volver = document.getElementById('volver-al-vivo');

  // Las URL llegan desde el HTML que renderiza el servidor, que es quien conoce
  // su propio árbol de rutas. Repetirlas acá crearía una segunda fuente de
  // verdad que puede divergir de la primera sin que nada se queje.
  var escenario = document.querySelector('.escenario');
  var urlPlaylist = escenario.dataset.playlist;
  var urlEventos = escenario.dataset.eventos;

  function mostrarFallo(texto) {
    fallo.textContent = texto;
    fallo.hidden = false;
  }

  // ---------- video ----------

  if (window.Hls && window.Hls.isSupported()) {
    var hls = new Hls({
      // Con una ventana de 3 segmentos —el mínimo que permite la spec de
      // HLS— esto posiciona al player al COMIENZO de la ventana, que es la
      // posición con más colchón disponible antes de quedarse sin datos.
      liveSyncDurationCount: 3,
      lowLatencyMode: false, // no aplica: esto no es LL-HLS
      enableWorker: true,    // el demux sale del hilo principal
    });

    hls.on(Hls.Events.ERROR, function (_evento, datos) {
      if (!datos.fatal) return;

      // Un 401 significa que la sesión venció mientras la pestaña estaba
      // abierta. Reintentar sería un bucle: hay que volver a entrar.
      var respuesta = datos.response;
      if (respuesta && respuesta.code === 401) {
        window.location.href = '/login';
        return;
      }

      switch (datos.type) {
        case Hls.ErrorTypes.NETWORK_ERROR:
          mostrarFallo('Problema de red. Reintentando…');
          hls.startLoad();
          break;
        case Hls.ErrorTypes.MEDIA_ERROR:
          // Típicamente la vuelta del ciclo: los timestamps saltan hacia
          // atrás y el decodificador necesita reinicializarse.
          mostrarFallo('Recuperando el video…');
          hls.recoverMediaError();
          break;
        default:
          mostrarFallo('No se pudo reproducir la transmisión.');
          hls.destroy();
      }
    });

    hls.on(Hls.Events.FRAG_BUFFERED, function () { fallo.hidden = true; });

    hls.loadSource(urlPlaylist);
    hls.attachMedia(video);
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    // Safari reproduce HLS de forma nativa y no necesita hls.js.
    video.src = urlPlaylist;
  } else {
    mostrarFallo('Este navegador no puede reproducir HLS.');
  }

  // "Volver al vivo": si el usuario pausa, queda atrás respecto del borde en
  // vivo. Se le ofrece el salto en vez de dejarlo mirando el pasado sin saberlo.
  video.addEventListener('timeupdate', function () {
    if (!video.seekable || video.seekable.length === 0) return;
    var borde = video.seekable.end(video.seekable.length - 1);
    volver.hidden = (borde - video.currentTime) < 12;
  });
  volver.addEventListener('click', function () {
    if (video.seekable && video.seekable.length > 0) {
      video.currentTime = video.seekable.end(video.seekable.length - 1);
    }
    video.play();
    volver.hidden = true;
  });

  // ---------- panel ----------

  var elEspectadores = document.getElementById('espectadores');
  var elSecuencia = document.getElementById('secuencia');
  var elVentana = document.getElementById('ventana');
  var elCuenta = document.getElementById('cuenta-regresiva');
  var elProgreso = document.getElementById('progreso');
  var elDisc = document.getElementById('discontinuidad');

  // Referencia de la cuenta regresiva. El servidor manda nextRotationMs una
  // vez por rotación; entre evento y evento interpolamos con el reloj local.
  // Pedirle al servidor la fracción que falta sería una petición por frame.
  var vencimiento = 0;
  var duracionTramo = 1;
  var ultimaSecuencia = null;

  var fuente = new EventSource(urlEventos);

  fuente.onmessage = function (mensaje) {
    var e;
    try {
      e = JSON.parse(mensaje.data);
    } catch (_) {
      return; // un evento ilegible no debe romper el panel
    }

    elEspectadores.textContent = e.viewers;
    elSecuencia.textContent = e.sequence;
    elDisc.hidden = !e.discontinuity;

    if (e.sequence !== ultimaSecuencia) {
      pintarVentana(e.window || []);
      ultimaSecuencia = e.sequence;
    }

    var falta = Math.max(0, e.nextRotationMs || 0);
    vencimiento = performance.now() + falta;
    duracionTramo = Math.max(falta, 1);
  };

  fuente.onerror = function () {
    // EventSource reconecta solo; sólo lo reflejamos en el panel. Un 401 tras
    // vencer la sesión termina en readyState CLOSED, y ahí sí hay que volver
    // a entrar.
    if (fuente.readyState === EventSource.CLOSED) {
      window.location.href = '/login';
    }
  };

  function pintarVentana(nombres) {
    elVentana.replaceChildren();
    nombres.forEach(function (nombre, i) {
      var li = document.createElement('li');
      // "segment14.ts" → "14": el panel es angosto y el prefijo es ruido.
      li.textContent = nombre.replace(/^segment/, '').replace(/\.ts$/, '');
      li.title = nombre;
      if (i === nombres.length - 1) li.classList.add('entrando');
      elVentana.appendChild(li);
    });
    // Quitar la clase en el siguiente frame dispara la transición del CSS.
    requestAnimationFrame(function () {
      var ultimo = elVentana.lastElementChild;
      if (ultimo) ultimo.classList.remove('entrando');
    });
  }

  function animar(ahora) {
    var restante = Math.max(0, vencimiento - ahora);
    elCuenta.textContent = (restante / 1000).toFixed(1) + ' s';
    elProgreso.style.width = (100 - (restante / duracionTramo) * 100).toFixed(1) + '%';
    requestAnimationFrame(animar);
  }
  requestAnimationFrame(animar);
})();
```

- [ ] **Step 7: Correr los tests y verificar que pasan**

Run: `go test ./internal/web/ -v -count=1`
Expected: PASS, toda la suite.

- [ ] **Step 8: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add internal/web
git commit -m "feat(web): player con hls.js vendorizado y panel en vivo

hls.js va commiteado (1.6.16, Apache-2.0) y no por CDN: el enunciado pide un
docker FUNCIONANDO, y un CDN deja la página con un video negro en cuanto el
contenedor no tiene internet. Un test recorre nuestros templates, CSS y JS
buscando referencias externas para que no vuelva a colarse una.

Los paneles de vidrio van al lado del video y nunca encima: backdrop-filter
recalcula el desenfoque a 25-30 fps sobre un video en reproducción y puede
tirar frames. Otro test lo verifica sobre el propio CSS, en vez de confiarlo
a la memoria de quien lo retoque.

La cuenta regresiva se interpola con requestAnimationFrame entre eventos SSE:
pedirle al servidor la fracción que falta sería una petición por frame."
```

---

### Task 8: `cmd/server` — cableado, limpieza de sesiones y apagado ordenado

**Files:**
- Create: `cmd/server/main.go`, `cmd/server/limpieza.go`
- Test: `cmd/server/limpieza_test.go`

**Interfaces:**
- Consumes: todo lo anterior más `config.Cargar`, `storage.Open/Migrate`, `hls.ParseManifest/New/Run`, `web.NewRouter/HookDeRotacion`.
- Produces: el binario. `go run ./cmd/server` levanta la aplicación completa.

**`Sessions.Limpiar` no tenía llamante y este bloque se lo da.** Sin una goroutine periódica, la tabla `sessions` crece indefinidamente: cada login deja una fila que ya nadie borra cuando vence. Es una fuga lenta pero real, y además **es el elemento asíncrono que aporta el bloque 03**, con la asincronía siendo criterio explícito de evaluación.

**`Engine.Run` se llama acá y en ningún otro lugar.** La garantía de un solo escritor sobre el estado del motor es una convención documentada, no forzada por código: dos goroutines corriendo `Run` romperían la monotonía de `EXT-X-MEDIA-SEQUENCE`.

**Por qué el servidor NO lleva `WriteTimeout`:** cortaría toda conexión SSE a los pocos segundos, justo el caso que este proyecto necesita mantener abierto. Contra clientes lentos se usa `ReadHeaderTimeout`, que ataca el problema real (slowloris) sin tocar las respuestas largas.

**Por qué el orden del apagado importa:** primero se cancela el contexto raíz, y eso hace que el hub cierre los canales de sus clientes; los handlers SSE ven el canal cerrado y vuelven. Recién entonces `Shutdown` puede terminar rápido. Al revés, `Shutdown` esperaría los 10 segundos completos a que se fueran solas unas conexiones que están diseñadas para no irse nunca.

**Por qué falla al arrancar si no hay segmentos:** un servidor arriba sirviendo 404 parece sano —responde, el healthcheck pasa— y el problema aparece recién cuando alguien intenta ver el stream. Un error en el arranque señala exactamente qué falta.

- [ ] **Step 1: Escribir el test de la limpieza**

Archivo `cmd/server/limpieza_test.go`:

```go
package main

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/storage/storagetest"
)

func TestLimpiarSesionesBorraLasVencidas(t *testing.T) {
	// Sin esta goroutine, la tabla sessions crece indefinidamente: cada login
	// deja una fila que nadie borra al vencer. Fuga lenta, pero real.
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()

	usuarios := cuenta.NewStore(db)
	u, err := cuenta.Registrar(ctx, usuarios, func(p string) (string, error) { return "hash-" + p, nil },
		"Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	// Una sesión emitida "ayer" con TTL de una hora ya está vencida.
	ayer := time.Now().Add(-24 * time.Hour)
	viejas := auth.NewSessions(db, time.Hour, auth.ConReloj(func() time.Time { return ayer }))
	if _, err := viejas.Crear(ctx, u.ID); err != nil {
		t.Fatalf("creando la sesión vencida: %v", err)
	}

	sesiones := auth.NewSessions(db, time.Hour)
	if n := contarSesiones(t, db); n != 1 {
		t.Fatalf("sesiones antes = %d, quiero 1", n)
	}

	ctxLimpieza, cancelar := context.WithCancel(ctx)
	defer cancelar()
	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		limpiarSesiones(ctxLimpieza, sesiones, 10*time.Millisecond, log.New(io.Discard, "", 0))
	}()

	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		if contarSesiones(t, db) == 0 {
			cancelar()
			select {
			case <-hecho:
				return // también comprueba que la goroutine termina al cancelar
			case <-time.After(time.Second):
				t.Fatal("limpiarSesiones no volvió tras cancelar el contexto")
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("la sesión vencida sigue en la base tras 2 s")
}

func TestLimpiarSesionesRespetaLasVigentes(t *testing.T) {
	// Un barrido que se llevara las sesiones vivas desconectaría a todo el
	// mundo cada pocos minutos.
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()

	usuarios := cuenta.NewStore(db)
	u, err := cuenta.Registrar(ctx, usuarios, func(p string) (string, error) { return "hash-" + p, nil },
		"Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	sesiones := auth.NewSessions(db, time.Hour)
	if _, err := sesiones.Crear(ctx, u.ID); err != nil {
		t.Fatalf("Crear: %v", err)
	}

	ctxLimpieza, cancelar := context.WithCancel(ctx)
	go limpiarSesiones(ctxLimpieza, sesiones, 5*time.Millisecond, log.New(io.Discard, "", 0))
	time.Sleep(100 * time.Millisecond)
	cancelar()

	if n := contarSesiones(t, db); n != 1 {
		t.Fatalf("sesiones vigentes = %d, quiero 1: la limpieza se llevó una sesión viva", n)
	}
}

func contarSesiones(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("contando sesiones: %v", err)
	}
	return n
}
```

Los imports del archivo son: `context`, `database/sql`, `io`, `log`, `testing`, `time`, más `zapping-live/internal/auth`, `zapping-live/internal/cuenta` y `zapping-live/internal/storage/storagetest`.

- [ ] **Step 2: Correr los tests y verificar que fallan**

Run: `go test ./cmd/server/ -v`
Expected: FAIL — `limpiarSesiones` no existe.

- [ ] **Step 3: Escribir `cmd/server/limpieza.go`**

```go
package main

import (
	"context"
	"log"
	"time"

	"zapping-live/internal/auth"
)

// limpiarSesiones borra periódicamente las sesiones vencidas.
//
// Le da llamante a Sessions.Limpiar, que hasta este bloque no tenía ninguno:
// sin esta goroutine la tabla `sessions` crece indefinidamente, porque cada
// login deja una fila que nadie borra cuando vence. Es una fuga lenta pero
// real, y en un servicio pensado para quedarse levantado importa.
//
// Vuelve cuando se cancela el contexto, que es lo que la ata al apagado
// ordenado del proceso en vez de dejarla corriendo hasta que el runtime muera.
func limpiarSesiones(ctx context.Context, s *auth.Sessions, cada time.Duration, registro *log.Logger) {
	// Un barrido de entrada, antes del primer tic: si el proceso estuvo caído
	// un día, arranca con la tabla llena de filas ya vencidas y esperar el
	// intervalo completo sería gratuito.
	barrer(ctx, s, registro)

	t := time.NewTicker(cada)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			barrer(ctx, s, registro)
		case <-ctx.Done():
			return
		}
	}
}

func barrer(ctx context.Context, s *auth.Sessions, registro *log.Logger) {
	n, err := s.Limpiar(ctx)
	if err != nil {
		// Un fallo acá no es motivo para tumbar el servicio: la próxima vuelta
		// lo reintenta. Pero se registra, o la tabla crecería en silencio.
		registro.Printf("limpieza de sesiones: %v", err)
		return
	}
	if n > 0 {
		registro.Printf("limpieza de sesiones: %d vencidas eliminadas", n)
	}
}
```

- [ ] **Step 4: Correr los tests y verificar que pasan**

Run: `go test ./cmd/server/ -v -count=1`
Expected: PASS, 2 tests.

- [ ] **Step 5: Escribir `cmd/server/main.go`**

```go
// Command server levanta el servicio de livestreaming.
//
// Es el único lugar donde se cablean los paquetes entre sí: cada uno de ellos
// recibe sus dependencias ya construidas y no busca ninguna por su cuenta.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"zapping-live/internal/auth"
	"zapping-live/internal/config"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/hls"
	"zapping-live/internal/storage"
	"zapping-live/internal/viewers"
	"zapping-live/internal/web"
)

// intervaloLimpieza es cada cuánto se barren las sesiones vencidas. Una hora
// alcanza de sobra: la tabla no crece rápido y el barrido es un DELETE sobre
// un índice.
const intervaloLimpieza = time.Hour

// plazoApagado es lo que se le da a las conexiones en curso al recibir la
// señal. Las de SSE ya se habrán ido por su cuenta: la cancelación del
// contexto cierra el hub y sus handlers vuelven.
const plazoApagado = 10 * time.Second

func main() {
	registro := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	if err := run(registro); err != nil {
		registro.Fatalf("el servidor no pudo continuar: %v", err)
	}
	registro.Print("apagado limpio")
}

func run(registro *log.Logger) error {
	cfg, err := config.Cargar()
	if err != nil {
		return fmt.Errorf("configuración: %w", err)
	}

	// NotifyContext ata SIGINT y SIGTERM al contexto raíz: `docker stop` y
	// Ctrl+C recorren exactamente el mismo camino de apagado.
	ctx, detener := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer detener()

	db, err := storage.Open(ctx, cfg.RutaDB)
	if err != nil {
		return fmt.Errorf("abriendo la base en %q: %w", cfg.RutaDB, err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrando la base: %w", err)
	}

	// Si no hay segmentos, NO se levanta. Un servidor arriba sirviendo 404
	// parece sano —responde, el healthcheck pasa— y el problema aparecería
	// recién cuando alguien intente ver el stream.
	manifiesto := filepath.Join(cfg.DirSegmentos, "segment.m3u8")
	pool, err := hls.ParseManifest(manifiesto)
	if err != nil {
		return fmt.Errorf("leyendo el manifiesto %q (¿corriste scripts/prepare-segments?): %w", manifiesto, err)
	}
	registro.Printf("pool cargado: %d segmentos desde %s", pool.Len(), cfg.DirSegmentos)

	hub := viewers.NewHub()

	// El hook conecta el motor con el hub sin que hls conozca al hub ni al
	// revés. Corre SÍNCRONAMENTE en la goroutine de rotación: Hub.Publicar
	// descarta en vez de bloquear justamente por esto.
	motor := hls.New(pool,
		hls.WithWindowSize(cfg.TamVentana),
		hls.WithRotationHook(web.HookDeRotacion(hub)),
	)

	usuarios := cuenta.NewStore(db)
	sesiones := auth.NewSessions(db, cfg.TTLSesion)
	guard := auth.NewGuard(sesiones, usuarios, cfg.CookiesSeguras)

	go hub.Run(ctx)
	// Engine.Run EXACTAMENTE UNA VEZ por instancia, y este es el único
	// llamante del proyecto: dos goroutines romperían la monotonía de
	// EXT-X-MEDIA-SEQUENCE, que es lo que el enunciado pide garantizar.
	go motor.Run(ctx)
	go limpiarSesiones(ctx, sesiones, intervaloLimpieza, registro)

	srv := &http.Server{
		Addr: ":" + cfg.Puerto,
		Handler: web.NewRouter(web.Deps{
			Motor:    motor,
			Pool:     pool,
			Hub:      hub,
			Guard:    guard,
			Sesiones: sesiones,
			Usuarios: usuarios,
			Salud:    db.PingContext,
			Log:      registro,
		}),
		// Contra slowloris, sin tocar las respuestas largas.
		ReadHeaderTimeout: 10 * time.Second,
		// SIN WriteTimeout a propósito: cortaría toda conexión SSE a los pocos
		// segundos, que es justo lo que este servicio necesita mantener
		// abierto. El problema que WriteTimeout resuelve —clientes que no
		// leen— ya está cubierto por el backpressure del hub.
	}

	errores := make(chan error, 1)
	go func() {
		registro.Printf("escuchando en http://localhost:%s", cfg.Puerto)
		errores <- srv.ListenAndServe()
	}()

	select {
	case err := <-errores:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("escuchando: %w", err)
		}
	case <-ctx.Done():
		registro.Print("señal recibida, apagando")
	}

	// El orden importa: al llegar acá el contexto raíz ya está cancelado, así
	// que el hub cerró los canales de sus clientes y los handlers SSE ya
	// volvieron. Si Shutdown fuera primero, esperaría el plazo completo a que
	// se cerraran unas conexiones diseñadas para no cerrarse nunca.
	ctxApagado, cancelar := context.WithTimeout(context.Background(), plazoApagado)
	defer cancelar()
	if err := srv.Shutdown(ctxApagado); err != nil {
		return fmt.Errorf("apagando el servidor: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Comprobar que el servidor levanta de verdad**

```bash
CGO_ENABLED=0 go build -o /dev/null ./...

# Con los segmentos reales del material provisto.
DB_PATH=./tmp-zapping.db SEGMENTS_DIR="./hls test" PORT=8080 go run ./cmd/server
```

En otra terminal:

```bash
curl -si localhost:8080/healthz            # 200 ok
curl -si localhost:8080/live/stream.m3u8   # 401: la ruta del stream está protegida
curl -si localhost:8080/ | head -3          # 302 → /login
```

Después, en el navegador: `http://localhost:8080/register` → crear cuenta → el player reproduce. Abrir una segunda pestaña y comprobar que el contador marca 2. `Ctrl+C` debe apagar sin colgarse.

Borrar la base de prueba al terminar: `rm -f ./tmp-zapping.db*`.

- [ ] **Step 7: Commitear**

```bash
gofmt -l . && go vet ./... && CGO_ENABLED=0 go build ./...
git add cmd .gitignore
git commit -m "feat(server): cableado, limpieza de sesiones y apagado ordenado

Sessions.Limpiar por fin tiene llamante: sin la goroutine periódica la tabla
sessions crece indefinidamente, porque cada login deja una fila que nadie
borra al vencer. Engine.Run se llama acá y en ningún otro lugar: dos
goroutines romperían la monotonía de EXT-X-MEDIA-SEQUENCE.

El http.Server no lleva WriteTimeout a propósito — cortaría toda conexión SSE
a los pocos segundos. Contra clientes lentos van ReadHeaderTimeout y el
backpressure del hub, que atacan el problema real.

En el apagado se cancela el contexto ANTES de Shutdown: así el hub cierra los
canales, los handlers SSE vuelven solos y Shutdown termina rápido en vez de
esperar el plazo completo a conexiones diseñadas para no cerrarse."
```

---

### Task 9: Reconciliar la documentación y verificar el bloque completo

**Files:**
- Modify: `docs/04-web-y-frontend.md`, `docs/06-decisiones.md`, `docs/00-indice.md`, `.gitignore`

**Interfaces:**
- Consumes: todo lo construido en las Tasks 1-8.
- Produces: documentación que describe el código que existe, y el registro de lo que hereda el bloque 05.

**Por qué esta tarea existe y no es opcional.** Pasó en los bloques 02 y 03: el documento de diseño se escribió antes que el código y quedó desfasado; hubo que reconciliarlo después, con el costo de leer dos versiones contradictorias de la misma cosa. Un documento que miente es peor que no tener documento, porque se lo cree. Y `docs/06-decisiones.md` es de donde salen el README y el correo de entrega: lo que no quede escrito acá no llega a la entrega.

- [ ] **Step 1: Verificación completa de la rama**

```bash
go test ./... -count=1
CGO_ENABLED=0 go build ./...
go vet ./...
gofmt -l .
```
Expected: todo verde, `gofmt -l` sin salida.

Comprobar que no entró ninguna dependencia nueva:

```bash
git diff master..HEAD -- go.mod go.sum
```
Expected: **sin cambios**. Si aparece algo, para y reporta.

Comprobar que la regla de dependencias se respeta:

```bash
go list -deps ./internal/hls    | grep -x net/http          # sin resultados
go list -deps ./internal/viewers | grep zapping-live         # sin resultados
```
Expected: ambos vacíos. `hls` no conoce HTTP y `viewers` no conoce ningún paquete del proyecto.

Detector de carreras, en contenedor Linux porque `-race` necesita un compilador C:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "C:/Users/Matias/Desktop/PruebaTecnica:/src" -w /src \
  golang:1.26 go test -race -count=1 ./...
```
Expected: todo `ok`, sin advertencias. **Anotar el resultado real**, incluido el número de tests, para citarlo en `docs/06`.

- [ ] **Step 2: Ignorar la base de datos de las pruebas manuales**

Anexar a `.gitignore`:

```gitignore
# Base de las pruebas manuales con `go run ./cmd/server`
tmp-zapping.db
tmp-zapping.db-shm
tmp-zapping.db-wal
```

- [ ] **Step 3: Reconciliar `docs/04-web-y-frontend.md` con el código**

Cinco correcciones, cada una donde corresponda:

1. **Sección "El player" y "Estilo":** cambiar `web/static` por `internal/web/static` y `templates/` por `internal/web/templates/`, y agregar el motivo: *"Los assets van embebidos con `go:embed`, y `embed` no puede subir de directorio. Beneficio adicional: el binario es autosuficiente y el Dockerfile no necesita `COPY web/`."*
2. **Sección "`internal/viewers` — el hub SSE":** corregir el bloque de firmas a lo que existe:

   ```go
   func NewHub() *Hub
   func (h *Hub) Run(ctx context.Context)              // dueña única del set de clientes
   func (h *Hub) Suscribir() (<-chan Evento, func())   // canal de sólo lectura + baja idempotente
   func (h *Hub) Publicar(e Evento)                    // NUNCA bloquea
   func (h *Hub) Espectadores() int64
   ```

   Y agregar el segundo punto de backpressure, que el doc no tenía: *"Hay **dos** lugares donde no se puede bloquear. El envío por cliente (el `select`/`default` ya documentado) y **`Publicar` mismo**, porque lo llama el hook síncrono del motor: bloquearlo detendría el avance del stream para todos."*
3. **Sección "El evento":** agregar que el hub completa `viewers` al difundir (el llamante no lo sabe) y que recuerda el último evento para entregárselo al instante a quien se conecta.
4. **Sección "Handlers de páginas":** agregar el párrafo que faltaba con las cuatro restricciones del bloque 03 — `cuenta.Registrar` (no `Store.Crear`), `auth.VerificarEnVacio()` en la rama del email inexistente, `DestruirDeUsuario` antes de `Crear`, y `Guard.PonerCookie`. Y que los errores de formulario responden **422**, no 200.
5. **Sección "El endpoint":** agregar el latido de 20 s y el `X-Accel-Buffering: no`, con su motivo.

Además, actualizar la tabla de tests para que liste los nombres reales (los de las Tasks 3 a 8) y marcar los criterios de aceptación que la verificación manual del Step 1 de la Task 8 ya confirmó.

- [ ] **Step 4: Registrar las decisiones del bloque en `docs/06-decisiones.md`**

Agregar a la sección "Decisiones técnicas":

```markdown
### El hub SSE tiene una goroutine dueña, no un mutex

**Alternativa evaluada:** un `map[*cliente]struct{}` protegido con `sync.RWMutex`.

**Decisión:** una sola goroutine (`Hub.Run`) es dueña del conjunto de clientes;
todo lo demás entra por canales.

**Por qué:** el mapa deja de necesitar sincronización porque nadie más lo toca,
y el alta, la baja y la difusión se serializan solas. Es el patrón que Go
recomienda para este caso y hace que el paquete se verifique con `-race` sin
ambigüedad. El costo es que las operaciones pasan por canales, irrelevante a
este ritmo (un evento cada ~10 s).

### Dos puntos de backpressure, no uno

El doc de diseño tenía uno; en la implementación resultaron ser dos, y por
razones distintas:

1. **El envío a cada cliente.** Un espectador que dejó de leer bloquearía el
   broadcast para todos y acumularía eventos sin techo.
2. **`Hub.Publicar`.** Lo llama el hook de rotación del motor, que corre
   síncronamente en la goroutine que hace avanzar el stream. Si se bloqueara,
   el stream se detendría **para todos los espectadores**.

Ambos descartan con `select`/`default`. Descartar es correcto porque cada
evento trae el estado completo, no un delta: el siguiente pone al día igual.

### El binario es autosuficiente: templates, CSS, JS y hls.js embebidos

`go:embed` mete los assets dentro del binario. El contenedor no depende de cuál
sea el directorio de trabajo, el Dockerfile no necesita `COPY web/`, y un
`go run ./cmd/server` desde cualquier carpeta funciona igual. Como `embed` no
puede subir de directorio, los assets viven bajo `internal/web/`.

### El servidor HTTP no lleva `WriteTimeout`

Cortaría toda conexión SSE a los pocos segundos, que es justo lo que este
servicio necesita mantener abierta. Contra clientes lentos van
`ReadHeaderTimeout` (slowloris) y el backpressure del hub, que atacan el
problema real sin romper las respuestas largas.

### Errores de formulario con 422, no con 200

Un 200 diría que la petición se procesó correctamente, y no es cierto. 422
(Unprocessable Content) es exacto y los navegadores renderizan el cuerpo igual.

### El `.m3u8` y los `.ts` se cachean al revés a propósito

El playlist con `no-cache, no-store`: uno cacheado le entrega al player una
ventana vieja, que lo lleva a pedir segmentos ya expirados y a cortar la
reproducción. Los segmentos con `max-age=31536000, immutable`: su contenido no
cambia nunca, así que revisitar la ventana no vuelve a costarle disco al
servidor.
```

Agregar a "Limitaciones conocidas":

```markdown
### El login cierra las sesiones de los otros dispositivos

`DestruirDeUsuario` antes de `Crear` previene session fixation, pero como
efecto colateral entrar desde el celular desconecta el navegador. Se aceptó a
propósito: en una prueba técnica pesa más la propiedad de seguridad demostrable
que la comodidad multi-dispositivo. Un producto real rotaría sólo el token y
dejaría `DestruirDeUsuario` para el cambio de contraseña.

### Sin token CSRF en los formularios

La cookie de sesión es `SameSite=Lax`, lo que impide que un sitio externo envíe
un POST autenticado, y las tres acciones con efecto (registro, login, logout)
son POST. Para un sitio de tres páginas es suficiente; un producto con más
superficie agregaría el token.
```

Reemplazar la sección "Restricciones que heredan los bloques siguientes" por lo que hereda el bloque 05:

```markdown
## Restricciones que hereda el bloque 05 (Docker y entrega)

- **El Dockerfile ya NO necesita `COPY web/`.** Los assets van embebidos en el
  binario. Copiar un directorio `web/` que no existe haría fallar el build.
- **`SEGMENTS_DIR` debe apuntar al directorio que contiene `segment.m3u8`.**
  El servidor arma la ruta como `filepath.Join(SEGMENTS_DIR, "segment.m3u8")` y
  **no levanta** si no lo encuentra. Es deliberado: un servidor arriba sirviendo
  404 parece sano y el problema aparecería recién al intentar ver el stream.
- **El healthcheck consulta la base.** Si el volumen de `/data` no es escribible
  por el usuario `app`, `/healthz` responde 503 y Docker marca el contenedor
  como unhealthy. Es la señal correcta, pero conviene saber que ese es el
  síntoma de un problema de permisos.
- **`SECURE_COOKIES=true` sin HTTPS por delante rompe el login**: la cookie no
  viaja y nadie puede entrar. El default es `false` por eso.
- **El apagado depende de que la señal llegue al proceso 1.** Con
  `ENTRYPOINT ["/app/server"]` (forma exec) llega; con la forma shell la
  intercepta `/bin/sh` y `docker stop` mataría el contenedor a los 10 s sin
  cerrar el WAL de SQLite.
```

Actualizar la sección de verificación con los comandos y resultados reales del Step 1.

- [ ] **Step 5: Actualizar `docs/00-indice.md`**

Marcar el bloque 04 como ✅ Completo en la tabla, y anotar en la fila del 05 que el `COPY web/` del diseño original ya no aplica.

- [ ] **Step 6: Commitear**

```bash
git add docs .gitignore
git commit -m "docs: cierra el bloque 04 y registra lo que hereda el bloque 05

Reconcilia 04-web-y-frontend.md con el código: assets embebidos, la firma real
de Hub.Suscribir, el segundo punto de backpressure que el diseño no tenía, las
cuatro restricciones del bloque 03 que los handlers cumplen, y el latido del
SSE. Un documento que miente es peor que no tener documento."
```

- [ ] **Step 7: Revisión de la rama completa**

Usar `superpowers:requesting-code-review` sobre `feat/web-frontend` contra `feat/auth-db`.

**Instrucción explícita para el revisor, porque es lo que encontró casi todo en los bloques 02 y 03:** *muta el código deliberadamente y comprueba si algún test se queja.* En particular:

- Quitar el `default` del `select` de `enviar` en `hub.go` → `TestHubClienteLentoNoBloqueaALosDemas` debe colgarse.
- Quitar el `default` del `select` de `Publicar` → `TestHookDeRotacionNoBloqueaConElHubDetenido` debe colgarse.
- Borrar la llamada a `auth.VerificarEnVacio()` → `TestLoginConEmailInexistentePagaBcrypt` debe fallar.
- Cambiar `cuenta.Registrar` por `p.usuarios.Crear(ctx, nombre, email, clave)` → `TestRegistroCreaLaCuentaYDejaSesionIniciada` debe fallar por contraseña en claro.
- Quitar `DestruirDeUsuario` → `TestLoginRotaLaSesion` debe fallar.
- Cambiar `no-store` por `max-age=10` en el playlist → `TestPlaylistCabeceras` debe fallar.
- Devolver el archivo con `io.Copy` en vez de `ServeContent` → `TestSegmentoSoportaRangos` debe fallar.
- Poner `backdrop-filter` en `.marco` → `TestNingunVidrioSeSuperponeAlVideo` debe fallar.
- Cambiar `/live/stream.m3u8` por `/stream.m3u8` → `TestPlaylistEsElDelSnapshot` debe fallar por las URI relativas.

Si alguna mutación **no** rompe ningún test, ese test miente y hay que arreglarlo. Y si el defecto venía del plan, corregir también el plan.

- [ ] **Step 8: Aplicar los hallazgos y cerrar**

Corregir lo que salga de la revisión, con un commit por hallazgo o un commit agrupado si son menores. Volver a correr la verificación completa del Step 1.

**No mergear a `master`.** El usuario hace el merge de todas las ramas al final, después de sus pruebas manuales end-to-end.

---

## Criterios de aceptación del bloque

- [ ] Las tres páginas del requisito 2 existen y funcionan (registro, login, player).
- [ ] Sólo usuarios registrados acceden a `/player`, `/live/stream.m3u8`, `/live/segments/*` y `/live/events`.
- [ ] El video se reproduce de forma continua, incluida la vuelta del ciclo y el segmento de 4,57 s.
- [ ] El panel muestra espectadores y `MEDIA-SEQUENCE` en tiempo real.
- [ ] Abrir dos pestañas sube el contador a 2; cerrar una lo baja a 1.
- [ ] Ningún elemento con `backdrop-filter` se superpone al `<video>` (verificado por test).
- [ ] Sin peticiones a dominios externos (verificado por test).
- [ ] Cerrar la pestaña no deja goroutines vivas (verificado por test).
- [ ] `go test ./... -count=1` en verde; `go vet` limpio; `gofmt -l .` sin salida.
- [ ] `CGO_ENABLED=0 go build ./...` compila.
- [ ] `go.mod` y `go.sum` sin cambios respecto de `master`.
- [ ] `-race` sin advertencias en contenedor Linux.
- [ ] `go run ./cmd/server` levanta y el flujo completo funciona en el navegador.
- [ ] `docs/04`, `docs/06` y `docs/00` describen el código que existe.
