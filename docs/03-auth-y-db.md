# 03 — Autenticación y base de datos

Paquetes `internal/storage`, `internal/cuenta`, `internal/auth`.
Cumple los requisitos 3 y 4 del enunciado.

## SQLite

`modernc.org/sqlite`: una traducción de SQLite a Go puro. Permite compilar con
`CGO_ENABLED=0` y obtener un **binario estático**, que es lo que hace posible una imagen
Docker mínima sin toolchain de C.

```go
// storage/db.go
func Open(ctx context.Context, path string) (*sql.DB, error)
```

Pragmas al abrir, cada uno por una razón:

| Pragma | Valor | Por qué |
| --- | --- | --- |
| `journal_mode` | `WAL` | Lecturas concurrentes sin bloquear la escritura |
| `busy_timeout` | `5000` | Espera en vez de fallar con `SQLITE_BUSY` |
| `foreign_keys` | `ON` | SQLite las ignora por defecto; sin esto el `ON DELETE CASCADE` no actúa |
| `synchronous` | `NORMAL` | Suficiente con WAL, y bastante más rápido que `FULL` |

SQLite es un archivo, no un servidor: el pool se mantiene chico. En concreto:
`SetMaxOpenConns(8)`, `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(5 * time.Minute)`. Con WAL no
hace falta forzar `SetMaxOpenConns(1)` para las escrituras: SQLite serializa los escritores
internamente y `busy_timeout` absorbe la contención en vez de devolver `SQLITE_BUSY`.

## Migraciones

Sin librería. Una tabla de versión y una lista de sentencias en `storage/migrate.go`,
aplicadas en una transacción al arrancar. Idempotente: correr el contenedor dos veces sobre
el mismo volumen no rompe nada.

```sql
CREATE TABLE IF NOT EXISTS schema_version (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

Las columnas de fecha (`created_at`, `expires_at`) son `INTEGER` con segundos Unix
(`time.Time.Unix()`), no `DATETIME`. SQLite no tiene un tipo de fecha nativo —
`DATETIME` es sólo una anotación de tipo que el driver puede interpretar como quiera — así
que guardar texto o un tipo "de fecha" ata la representación en disco a cómo serialice ese
driver en particular. Un entero es inequívoco, ordena correctamente con `<`/`>` para las
consultas de expiración, y `time.Unix(n, 0)` lo reconstruye sin ambigüedad en cualquier
lenguaje o driver que lea la base.

El email se normaliza a minúsculas y sin espacios **antes** de guardar, para que el
`UNIQUE` funcione de verdad y no deje pasar `Ana@x.com` junto a `ana@x.com`.

## `internal/cuenta`

El paquete se llama `cuenta` y el tipo es `Usuario`, no `user.User`: en español de punta a
punta, como el resto del dominio.

```go
type Usuario struct {
    ID        int64
    Name      string
    Email     string
    CreatedAt time.Time
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store

func (s *Store) Crear(ctx context.Context, name, email, hash string) (*Usuario, error)
func (s *Store) PorEmail(ctx context.Context, email string) (*Usuario, string, error) // usuario + hash
func (s *Store) PorID(ctx context.Context, id int64) (*Usuario, error)

func Registrar(ctx context.Context, s *Store, hash func(string) (string, error),
    name, email, password string) (*Usuario, error)
```

`Crear` es la vía de bajo nivel: recibe el hash ya calculado y no valida nada, así que nada
impide llamarla con una contraseña en claro en el tercer argumento. Existe para que los tests
puedan poblar filas sin pagar el costo de bcrypt. **El alta normal de un usuario pasa por
`Registrar`**, que valida (`Validar`), hashea (con la función que se le inyecte, típicamente
`auth.HashPassword`) y persiste en un solo paso — así ningún punto de entrada puede saltarse
la validación. `Registrar` recibe la función de hash en vez de importar `auth` directamente
porque `auth` ya importa `cuenta` (para `cuenta.Store` y `cuenta.Usuario` en el guard); importar
en el otro sentido sería un ciclo.

`Usuario` **no tiene campo de contraseña**. El hash se devuelve aparte, sólo donde se necesita
verificarlo. Así es imposible filtrarlo por accidente al renderizar un template o serializar
a JSON.

### Validación

En `cuenta/cuenta.go`, no en los handlers, para que sea la misma en cualquier punto de entrada.
Eso sólo se sostiene si hay un único camino para dar de alta un usuario: `Registrar` es ese
camino (ver arriba) y es quien de verdad llama a `Validar` antes de persistir — `Store.Crear`
por sí sola no lo hace.

| Campo | Regla | Mensaje |
| --- | --- | --- |
| Nombre | No vacío, ≤ 100 caracteres | "El nombre es obligatorio" |
| Email | `net/mail.ParseAddress` + coincide con `addr.Address`, ≤ 254 caracteres | "El email no es válido" |
| Contraseña | 8 a 72 caracteres | "La contraseña debe tener al menos 8 caracteres" |

`net/mail.ParseAddress` por sí solo no basta: acepta formas como `"Nombre <ana@x.com>"`,
`"<ana@x.com>"` o `"ana@x.com (comentario)"` y las deja pasar sin avisar. Si esas formas se
guardaran tal cual, `NormalizarEmail("Nombre <ana@x.com>")` no colisionaría con
`NormalizarEmail("ana@x.com")` en el `UNIQUE` de la base — dos altas para la misma persona,
y un login que no encuentra la cuenta según con qué forma se registró. Por eso, además de que
`ParseAddress` no falle, se exige que la dirección ya extraída (`addr.Address`) coincida
exactamente con lo que se recibió: eso descarta cualquier envoltorio y garantiza la dirección
desnuda.

El tope de 72 no es arbitrario: **bcrypt trunca silenciosamente en 72 bytes.** Sin este
límite, dos contraseñas largas que compartan los primeros 72 bytes serían equivalentes.
Rechazarlas explícitamente es preferible a truncar sin avisar.

## `internal/auth` — contraseñas

```go
// auth/password.go
const CostoBcrypt = 12

func HashPassword(plain string) (string, error)      // bcrypt.GenerateFromPassword, costo CostoBcrypt
func VerifyPassword(hash, plain string) bool         // bcrypt.CompareHashAndPassword
func VerificarEnVacio()                              // compara contra un hash de referencia, mismo costo
```

Costo 12: el punto de equilibrio recomendado entre resistencia a fuerza bruta y latencia
tolerable en un login. `TestCostoDeProduccionEs12` es el único test que paga el costo real (los
demás usan `bcrypt.MinCost` para no volver lenta la suite) y sirve de referencia medida: ronda
los **~0,37 s** en la máquina donde corre esta suite — coherente con el orden de magnitud
esperado para costo 12, aunque el número exacto depende del hardware.

bcrypt genera la sal y codifica el costo **dentro del propio string del hash**, así que se
guarda una sola columna y no hay formato propio que mantener.

**Enumeración de usuarios por tiempo:** el mensaje de error del login es genérico tanto si el
email no existe como si la contraseña es incorrecta, pero eso no alcanza por sí solo: un email
inexistente nunca llega a bcrypt (responde en microsegundos) mientras uno existente paga el
costo real de `VerifyPassword` (el orden de los ~0,37 s medidos arriba). El reloj delata la
diferencia aunque el mensaje sea idéntico. `VerificarEnVacio()` compara contra un hash de
referencia (generado una única vez al cargar el paquete, con el mismo `CostoBcrypt`) para que
el handler de login pague ese mismo costo cuando el email no existe. Generar el hash de
referencia en cada llamada en vez de una sola vez duplicaría el costo de esa rama y
reintroduciría la misma asimetría que la función busca evitar.

## `internal/auth` — sesiones

Implementación propia sobre stdlib. Sin `gorilla/sessions`.

```go
// auth/session.go
type Sessions struct {
    db  *sql.DB
    ttl time.Duration
    now func() time.Time
}

func NewSessions(db *sql.DB, ttl time.Duration, opts ...OpcionSesion) *Sessions
func ConReloj(now func() time.Time) OpcionSesion   // inyecta el reloj para tests

func (s *Sessions) TTL() time.Duration                                              // única fuente de verdad del TTL
func (s *Sessions) Crear(ctx context.Context, userID int64) (token string, err error)
func (s *Sessions) Resolver(ctx context.Context, token string) (userID int64, ok bool, err error)
func (s *Sessions) Destruir(ctx context.Context, token string) error
func (s *Sessions) DestruirDeUsuario(ctx context.Context, userID int64) error   // previene session fixation
func (s *Sessions) Limpiar(ctx context.Context) (n int64, err error)           // borra expiradas
```

`Resolver` distingue "no hay sesión" de "la base falló": `(0, false, nil)` es la respuesta
normal ante un token inválido o expirado; el error sólo viaja cuando la consulta a la base
falla de verdad (por ejemplo, SQLite caído). Colapsar ambos casos en un mismo `(0, false)`
—como hacía una versión anterior— hacía que `RequirePage` mandara a `/login` tanto por falta de
sesión como por una base caída: el usuario reintenta, vuelve a fallar, y entra en un bucle de
redirección sin una sola línea de log que explique la causa real. `Guard.proteger` loguea ese
error con `log.Printf` antes de rechazar la petición.

**Ciclo de vida del token:**

```text
Crear:    32 bytes de crypto/rand → base64url → token (va al cliente)
                                  → SHA-256   → token_hash (va a la DB)

Resolver: token del cliente → SHA-256 → lookup por PK → verifica expires_at
```

Se guarda el hash y no el token por la misma razón que con las contraseñas: quien se lleve
la base obtiene valores inservibles. Y como el lookup es por clave primaria sobre un hash de
longitud fija, no hay riesgo de timing en la búsqueda.

`Limpiar` está pensada para correr en una goroutine periódica (p. ej. cada hora, cancelable
por contexto) desde donde se arranca el servidor. Sin eso la tabla `sessions` crece
indefinidamente — una fuga lenta pero real.

**La cookie:**

```go
http.Cookie{
    Name:     "zapping_session",
    Value:    token,
    Path:     "/",
    HttpOnly: true,                    // inaccesible desde JavaScript
    SameSite: http.SameSiteLaxMode,    // mitiga CSRF en navegación cruzada
    Secure:   cfg.SecureCookies,       // true tras HTTPS; configurable para probar en local
    MaxAge:   int(g.sesiones.TTL().Seconds()),
}
```

El TTL sale de `Sessions.TTL()`, no se recibe por parámetro en `PonerCookie`: si `Guard` y
`Sessions` tuvieran cada uno su propia copia del TTL, un descuido al construirlos con valores
distintos dejaría la cookie y la fila en la base caducando en momentos diferentes. Una sola
fuente de verdad evita esa clase de bug por construcción.

## Middleware

```go
// auth/guard.go
const NombreCookie = "zapping_session"

type Guard struct{ /* sesiones, usuarios, cookiesSeguras */ }

func NewGuard(s *Sessions, u *cuenta.Store, cookiesSeguras bool) *Guard

func (g *Guard) RequirePage(next http.Handler) http.Handler   // sin sesión → 302 a /login
func (g *Guard) RequireAPI(next http.Handler) http.Handler    // sin sesión → 401
func (g *Guard) PonerCookie(w http.ResponseWriter, token string)   // TTL sale de g.sesiones.TTL()
func (g *Guard) BorrarCookie(w http.ResponseWriter)

func UsuarioDe(ctx context.Context) (*cuenta.Usuario, bool)
```

El diseño original proponía un único middleware que mirara la cabecera `Accept: text/html`
para decidir entre redirigir y devolver `401`. Se descartó: ese comportamiento dependería de
un valor que el **cliente** controla y puede omitir sin querer. Un `curl` sin cabeceras a
`/player` recibiría un `302` a HTML donde en realidad no hay navegador para seguirlo, y un
cliente HLS que sí mande `Accept: text/html` (o ninguno) sobre `/live/stream.m3u8` podría
terminar recibiendo la redirección en vez del `401` esperado. Inspeccionar una cabecera
opcional para decidir el modo de fallo de una ruta protegida es construir la seguridad sobre
un dato que no es confiable.

En su lugar hay **dos middlewares explícitos**, y es el router — no una heurística sobre la
petición — el que decide cuál aplica a cada ruta:

- **`RequirePage`**, para páginas HTML: sin sesión válida, `302` a `/login`.
- **`RequireAPI`**, para el playlist, los segmentos y el SSE: sin sesión válida, `401` sin
  cuerpo.

La distinción importa de verdad: un `302` sobre `/live/stream.m3u8` haría que hls.js intente
parsear la página de login como playlist HLS y reporte un error incomprensible. Con `401`, el
player falla claro y el frontend puede reaccionar mandando al usuario al login.

Ambos middlewares comparten la misma resolución de sesión (`proteger`); sólo cambia qué se
hace cuando no hay una válida. Tras resolver la sesión, el guard también verifica que el
usuario todavía exista con `usuarios.PorID`: una sesión puede sobrevivir a su usuario si la
fila se borró sin pasar por el `ON DELETE CASCADE` (por ejemplo, en una migración manual), y
servir esa sesión huérfana — dejando pasar una petición autenticada como un usuario que ya no
existe — sería un fallo de seguridad, no un detalle cosmético.

Con sesión válida, ambos middlewares dejan el `*cuenta.Usuario` en el contexto del request,
recuperable con `UsuarioDe`.

## Rotación de sesión en el login

El handler de login (paquete web, plan siguiente) debe llamar `DestruirDeUsuario` antes de
`Crear` al autenticar: así se descartan las sesiones previas del usuario y se emite un token
nuevo. Previene *session fixation*: un token entregado antes de autenticarse nunca queda
válido después.

## Tests

**`internal/auth`** — contraseñas, sesiones, el guard y la cadena completa (`password_test.go`,
`session_test.go`, `guard_test.go`, `integration_test.go`):

| Test | Qué verifica |
| --- | --- |
| `TestVerifyPassword` | Un hash valida su contraseña y rechaza otra |
| `TestHashDistintoCadaVez` | Dos hashes de la misma contraseña difieren (la sal se aplica) |
| `TestHashNoContieneLaContraseña` | El string del hash no contiene la contraseña en claro |
| `TestVerifyPasswordHashInvalido` | Un hash corrupto no revienta, sólo devuelve `false` |
| `TestCostoDeProduccionEs12` | `HashPassword` usa `CostoBcrypt = 12`, no el costo mínimo de test |
| `TestVerificarEnVacioUsaCostoDeProduccion` | El hash de referencia de `VerificarEnVacio` es de costo 12; no mide tiempos (ver más arriba por qué) |
| `TestTTL` | `Sessions.TTL()` devuelve la vigencia configurada |
| `TestCrearYResolver` | Un token recién creado resuelve al usuario correcto |
| `TestTokensDistintosCadaVez` | Dos tokens seguidos no coinciden (entropía real) |
| `TestElTokenNoSeGuardaEnClaro` | La columna `token_hash` no contiene el token emitido |
| `TestSesionExpirada` | Dentro del TTL resuelve; **exactamente** en el borde del TTL, `ok=false` |
| `TestSesionExpiradaPasadoElBorde` | Bien pasado el TTL, `ok=false` |
| `TestResolverTokenInvalido` | Un token que no existe no resuelve |
| `TestDestruir` | Tras `Destruir` el token deja de resolver |
| `TestDestruirDeUsuario` | Borra las sesiones de un usuario sin tocar las de un SEGUNDO usuario (`WHERE user_id` protegido) |
| `TestLimpiarBorraSoloLasExpiradas` | Borra sólo las expiradas y deja intactas las vigentes |
| `TestRequirePageSinCookieRedirige` | Sin cookie, `RequirePage` → 302 a `/login` |
| `TestRequireAPISinCookieDevuelve401` | Sin cookie, `RequireAPI` → 401, sin `Location` |
| `TestConSesionValidaPasaYPoneElUsuarioEnContexto` | Con sesión, ambos middlewares dejan pasar y ponen el usuario en el contexto |
| `TestTokenInventadoNoPasa` | Un token inventado no da acceso |
| `TestSesionHuerfanaNoPasa` | Sesión cuyo usuario fue borrado: el guard rechaza, no explota |
| `TestCookieTieneLosAtributosDeSeguridad` | `PonerCookie` usa `HttpOnly`, `SameSite=Lax`, `Path=/` y el `MaxAge` correcto (TTL de `Sessions.TTL()`) |
| `TestBorrarCookieLaExpira` | `BorrarCookie` pone `MaxAge` negativo |
| `TestUsuarioDeSinContexto` | Un contexto sin usuario devuelve `ok=false` |
| `TestFlujoCompletoDeAutenticacion` | Cadena completa: `Registrar` → `password_hash` empieza con `$2` → login válido pasa `RequirePage` → contraseña incorrecta no valida → logout invalida la cookie |

**`internal/cuenta`** — el modelo y su store (`cuenta_test.go`, `store_test.go`):

| Test | Qué verifica |
| --- | --- |
| `TestNormalizarEmail` | Minúsculas y sin espacios |
| `TestValidarAceptaDatosBuenos` | Un alta correcta no dispara ningún `ErrorValidacion` |
| `TestValidarRechaza` | Cada regla de validación con su mensaje |
| `TestValidarPasswordMuyLargaSeRechaza` | > 72 bytes se rechaza en validación, no se trunca |
| `TestValidarLimiteEnBytesNoEnRunas` | El límite de contraseña es en bytes, no en runas (multibyte) |
| `TestValidarRechazaEmailConEnvoltorio` | `"Nombre <ana@x.com>"` no pasa la validación aunque `ParseAddress` lo acepte |
| `TestCrearYRecuperar` | Un usuario se puede crear y volver a leer |
| `TestCrearNormalizaElEmail` | El email queda normalizado en la base, no como se escribió |
| `TestCrearEmailDuplicado` | `Ana@X.com` y `ana@x.com` colisionan en el `UNIQUE` (`ErrEmailEnUso`) |
| `TestPorEmailInexistente` | Devuelve `ErrNoEncontrado`, no un error genérico |
| `TestPorID` | Recupera un usuario por su clave primaria |
| `TestRegistrarRechazaDatosInvalidosSinTocarLaBase` | Datos inválidos: `Registrar` no llama a la función de hash ni inserta ninguna fila |
| `TestRegistrarValidaHasheaYPersiste` | `Registrar` normaliza el email y persiste lo que devuelve la función de hash |
| `TestCreatedAtRoundTripEntreCrearYLecturas` | El `CreatedAt` de `Crear` coincide con el que devuelven `PorEmail` y `PorID` |
| `TestUserNoExponeElHash` | `Usuario` no tiene ningún campo con la contraseña o su hash |

Los tests de `internal/storage`, `internal/cuenta` y `internal/auth` abren la base con
`storagetest.Abrir`/`AbrirMigrada`, que crea un **archivo temporal propio** (`os.MkdirTemp`,
no `t.TempDir()`) — y nunca `:memory:`. Con un pool de conexiones cada conexión nueva abriría
su propia base en memoria independiente (SQLite en memoria es por-conexión, no compartida), así
que dos consultas del mismo test podrían terminar viendo datos distintos. Además
`journal_mode=WAL`, uno de los pragmas que se fija al abrir, no aplica a bases en memoria. Un
archivo en un directorio temporal da el comportamiento real sin dejar residuos.

El directorio se borra explícitamente en `t.Cleanup` con reintentos con backoff exponencial, en
vez de dejar que `t.TempDir()` lo borre por su cuenta: en Windows, al cerrar el último handle de
SQLite, el antivirus abre el archivo un instante para escanearlo, y el `rmdir` del directorio
puede fallar con `ERROR_DIR_NOT_EMPTY` — una clase de error que `testing.removeAll` no reintenta
(sólo reintenta `ACCESS_DENIED` y `SHARING_VIOLATION`). Sin este arreglo, la suite fallaba de
forma intermitente (~0,5 % de los directorios) con
`TempDir RemoveAll cleanup: unlinkat ...: The directory is not empty`. Verificado con
`go test ./internal/storage/... -count=50` sin fallos.

El TTL de sesión se inyecta (`ConReloj`) para poder probar la expiración sin esperar el TTL
real; ningún test de la suite espera más de un segundo real.

## Criterios de aceptación

- [ ] Un usuario puede registrarse, cerrar el navegador y volver a entrar.
- [ ] Dos registros con el mismo email dan error claro, no un 500.
- [ ] La contraseña nunca aparece en la base, ni en logs, ni en el HTML renderizado.
- [ ] `GET /player` sin sesión redirige a `/login`.
- [ ] `GET /live/stream.m3u8` sin sesión devuelve 401.
- [ ] Tras `POST /logout` la cookie deja de servir.
- [ ] Ninguna dependencia de sesiones o routing fuera de la stdlib.
