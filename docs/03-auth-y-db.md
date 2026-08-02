# 03 — Autenticación y base de datos

Paquetes `internal/storage`, `internal/user`, `internal/auth`.
Cumple los requisitos 3 y 4 del enunciado.

## SQLite

`modernc.org/sqlite`: una traducción de SQLite a Go puro. Permite compilar con
`CGO_ENABLED=0` y obtener un **binario estático**, que es lo que hace posible una imagen
Docker mínima sin toolchain de C.

```go
// storage/db.go
func Open(path string) (*sql.DB, error)
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
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT     NOT NULL,
    email         TEXT     NOT NULL UNIQUE,
    password_hash TEXT     NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    token_hash TEXT     PRIMARY KEY,
    user_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

El email se normaliza a minúsculas y sin espacios **antes** de guardar, para que el
`UNIQUE` funcione de verdad y no deje pasar `Ana@x.com` junto a `ana@x.com`.

## `internal/user`

```go
type User struct {
    ID        int64
    Name      string
    Email     string
    CreatedAt time.Time
}

type Store struct{ db *sql.DB }

func (s *Store) Create(ctx context.Context, name, email, hash string) (*User, error)
func (s *Store) ByEmail(ctx context.Context, email string) (*User, string, error) // user + hash
func (s *Store) ByID(ctx context.Context, id int64) (*User, error)
```

`User` **no tiene campo de contraseña**. El hash se devuelve aparte, sólo donde se necesita
verificarlo. Así es imposible filtrarlo por accidente al renderizar un template o serializar
a JSON.

### Validación

En `user/user.go`, no en los handlers, para que sea la misma en cualquier punto de entrada:

| Campo | Regla | Mensaje |
| --- | --- | --- |
| Nombre | No vacío, ≤ 100 caracteres | "El nombre es obligatorio" |
| Email | `net/mail.ParseAddress`, ≤ 254 caracteres | "El email no es válido" |
| Contraseña | 8 a 72 caracteres | "La contraseña debe tener al menos 8 caracteres" |

El tope de 72 no es arbitrario: **bcrypt trunca silenciosamente en 72 bytes.** Sin este
límite, dos contraseñas largas que compartan los primeros 72 bytes serían equivalentes.
Rechazarlas explícitamente es preferible a truncar sin avisar.

## `internal/auth` — contraseñas

```go
// auth/password.go
func HashPassword(plain string) (string, error)      // bcrypt.GenerateFromPassword, cost 12
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
}

func (s *Sessions) Create(ctx context.Context, userID int64) (token string, err error)
func (s *Sessions) Resolve(ctx context.Context, token string) (userID int64, ok bool)
func (s *Sessions) Destroy(ctx context.Context, token string) error
func (s *Sessions) Cleanup(ctx context.Context) error   // borra expiradas
```

**Ciclo de vida del token:**

```text
Create:  32 bytes de crypto/rand → base64url → token (va al cliente)
                                 → SHA-256   → token_hash (va a la DB)

Resolve: token del cliente → SHA-256 → lookup por PK → verifica expires_at
```

Se guarda el hash y no el token por la misma razón que con las contraseñas: quien se lleve
la base obtiene valores inservibles. Y como el lookup es por clave primaria sobre un hash de
longitud fija, no hay riesgo de timing en la búsqueda.

`Cleanup` corre en una goroutine cada hora, cancelable por contexto. Sin eso la tabla
`sessions` crece indefinidamente — una fuga lenta pero real.

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
// auth/middleware.go
func (s *Sessions) RequireAuth(next http.Handler) http.Handler
func UserFrom(ctx context.Context) (*user.User, bool)
```

Lee la cookie, resuelve la sesión, mete el usuario en el contexto del request y delega.
Si no hay sesión válida:

- **Peticiones de página** (`Accept: text/html`) → `302` a `/login`.
- **Peticiones del stream** (`.m3u8`, `.ts`, SSE) → `401` sin cuerpo.

La distinción importa: redirigir un `.m3u8` a una página de login haría que hls.js intente
parsear HTML como playlist y reporte un error incomprensible. Con `401`, el player falla
claro y el frontend puede reaccionar mandando al usuario al login.

## Rotación de sesión en el login

Al iniciar sesión se crea un token nuevo y se descarta el anterior si existía. Previene
*session fixation*: un token entregado antes de autenticarse nunca queda válido después.

## Tests

| Test | Qué verifica |
| --- | --- |
| `TestHashVerify` | Un hash válida su contraseña y rechaza otra |
| `TestHashDistintoCadaVez` | Dos hashes de la misma contraseña difieren (la sal se aplica) |
| `TestPasswordMuyLarga` | > 72 caracteres se rechaza en validación, no se trunca |
| `TestCreateResolve` | Un token recién creado resuelve al usuario correcto |
| `TestTokenNoSeGuardaEnClaro` | La columna `token_hash` no contiene el token emitido |
| `TestSesionExpirada` | Pasado el TTL, `Resolve` devuelve `ok=false` |
| `TestDestroy` | Tras `Destroy` el token deja de resolver |
| `TestCleanup` | Borra sólo las expiradas y deja intactas las vigentes |
| `TestEmailNormalizado` | `Ana@X.com` y `ana@x.com` colisionan en el `UNIQUE` |
| `TestMiddlewareSinCookie` | HTML → 302; `.m3u8` → 401 |
| `TestMiddlewareTokenInvalido` | Un token inventado no pasa |
| `TestValidacionUsuario` | Cada regla de validación con su mensaje |

Los tests usan SQLite en memoria (`:memory:`), así que no tocan disco ni requieren limpieza.
El TTL de sesión se inyecta para poder probar la expiración sin esperar.

## Criterios de aceptación

- [ ] Un usuario puede registrarse, cerrar el navegador y volver a entrar.
- [ ] Dos registros con el mismo email dan error claro, no un 500.
- [ ] La contraseña nunca aparece en la base, ni en logs, ni en el HTML renderizado.
- [ ] `GET /player` sin sesión redirige a `/login`.
- [ ] `GET /live/stream.m3u8` sin sesión devuelve 401.
- [ ] Tras `POST /logout` la cookie deja de servir.
- [ ] Ninguna dependencia de sesiones o routing fuera de la stdlib.
