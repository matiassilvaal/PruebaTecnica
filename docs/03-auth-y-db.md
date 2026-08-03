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

`db.SetMaxOpenConns(1)` para las escrituras no es necesario con WAL, pero sí conviene
`SetMaxIdleConns` razonable. SQLite es un archivo, no un servidor: el pool se mantiene chico.

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
```

`Usuario` **no tiene campo de contraseña**. El hash se devuelve aparte, sólo donde se necesita
verificarlo. Así es imposible filtrarlo por accidente al renderizar un template o serializar
a JSON.

### Validación

En `cuenta/cuenta.go`, no en los handlers, para que sea la misma en cualquier punto de entrada:

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
```

Costo 12: unos ~250 ms por verificación en hardware moderno, que es el punto de equilibrio
recomendado entre resistencia a fuerza bruta y latencia tolerable en un login.

bcrypt genera la sal y codifica el costo **dentro del propio string del hash**, así que se
guarda una sola columna y no hay formato propio que mantener.

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

func (s *Sessions) Crear(ctx context.Context, userID int64) (token string, err error)
func (s *Sessions) Resolver(ctx context.Context, token string) (userID int64, ok bool)
func (s *Sessions) Destruir(ctx context.Context, token string) error
func (s *Sessions) DestruirDeUsuario(ctx context.Context, userID int64) error   // previene session fixation
func (s *Sessions) Limpiar(ctx context.Context) (n int64, err error)           // borra expiradas
```

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
    MaxAge:   int(ttl.Seconds()),
}
```

## Middleware

```go
// auth/guard.go
const NombreCookie = "zapping_session"

type Guard struct{ /* sesiones, usuarios, cookiesSeguras */ }

func NewGuard(s *Sessions, u *cuenta.Store, cookiesSeguras bool) *Guard

func (g *Guard) RequirePage(next http.Handler) http.Handler   // sin sesión → 302 a /login
func (g *Guard) RequireAPI(next http.Handler) http.Handler    // sin sesión → 401
func (g *Guard) PonerCookie(w http.ResponseWriter, token string, ttl time.Duration)
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

**`internal/auth`** — contraseñas, sesiones y el guard (`password_test.go`, `session_test.go`,
`guard_test.go`):

| Test | Qué verifica |
| --- | --- |
| `TestVerifyPassword` | Un hash valida su contraseña y rechaza otra |
| `TestHashDistintoCadaVez` | Dos hashes de la misma contraseña difieren (la sal se aplica) |
| `TestHashNoContieneLaContraseña` | El string del hash no contiene la contraseña en claro |
| `TestVerifyPasswordHashInvalido` | Un hash corrupto no revienta, sólo devuelve `false` |
| `TestCostoDeProduccionEs12` | `HashPassword` usa `CostoBcrypt = 12`, no el costo mínimo de test |
| `TestCrearYResolver` | Un token recién creado resuelve al usuario correcto |
| `TestTokensDistintosCadaVez` | Dos tokens seguidos no coinciden (entropía real) |
| `TestElTokenNoSeGuardaEnClaro` | La columna `token_hash` no contiene el token emitido |
| `TestSesionExpirada` | Pasado el TTL, `Resolver` devuelve `ok=false` |
| `TestResolverTokenInvalido` | Un token que no existe no resuelve |
| `TestDestruir` | Tras `Destruir` el token deja de resolver |
| `TestDestruirDeUsuario` | Borra todas las sesiones de un usuario, no las de otros |
| `TestLimpiarBorraSoloLasExpiradas` | Borra sólo las expiradas y deja intactas las vigentes |
| `TestRequirePageSinCookieRedirige` | Sin cookie, `RequirePage` → 302 a `/login` |
| `TestRequireAPISinCookieDevuelve401` | Sin cookie, `RequireAPI` → 401, sin `Location` |
| `TestConSesionValidaPasaYPoneElUsuarioEnContexto` | Con sesión, ambos middlewares dejan pasar y ponen el usuario en el contexto |
| `TestTokenInventadoNoPasa` | Un token inventado no da acceso |
| `TestSesionHuerfanaNoPasa` | Sesión cuyo usuario fue borrado: el guard rechaza, no explota |
| `TestCookieTieneLosAtributosDeSeguridad` | `PonerCookie` usa `HttpOnly`, `SameSite=Lax`, `Path=/` y el `MaxAge` correcto |
| `TestBorrarCookieLaExpira` | `BorrarCookie` pone `MaxAge` negativo |
| `TestUsuarioDeSinContexto` | Un contexto sin usuario devuelve `ok=false` |

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
| `TestUserNoExponeElHash` | `Usuario` no tiene ningún campo con la contraseña o su hash |

Los tests de `internal/storage`, `internal/cuenta` y `internal/auth` abren la base con
`storagetest.Abrir`/`AbrirMigrada`, que crea un **archivo temporal** vía `t.TempDir()` — no
`:memory:`. Con un pool de conexiones cada conexión nueva abriría su propia base en memoria
independiente (SQLite en memoria es por-conexión, no compartida), así que dos consultas del
mismo test podrían terminar viendo datos distintos. Además `journal_mode=WAL`, uno de los
pragmas que se fija al abrir, no aplica a bases en memoria. Un archivo en un directorio
temporal (borrado automáticamente al terminar el test) da el comportamiento real sin dejar
residuos.

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
