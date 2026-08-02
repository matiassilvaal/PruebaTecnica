# 01 — Diseño

Spec acordado del proyecto. Los documentos 02–05 detallan la implementación de cada bloque.

## Objetivo

Un servicio en Go que genera un **livestreaming HLS simulado** a partir de segmentos de
video pregrabados, reproducible únicamente por usuarios registrados, entregado como una
imagen Docker que funciona con un comando.

## Arquitectura general

Un solo binario, un solo contenedor. Sin servicios externos, sin `docker-compose`.

```text
                          ┌───────────────────────────────┐
   navegador              │        binario Go             │
  ┌──────────┐            │                               │
  │ hls.js   │──GET .m3u8─┤  web ──► hls (motor)          │
  │ <video>  │──GET .ts───┤   │                           │
  │ EventSrc │──GET SSE───┤   ├──► viewers (hub)          │
  └──────────┘            │   └──► auth ──► storage       │
                          │                    │          │
                          └────────────────────┼──────────┘
                                          SQLite (volumen)
                                          segmentos (imagen)
```

## Estructura de paquetes

```text
zapping-live/
├── cmd/server/main.go          wiring, config, shutdown ordenado
├── internal/
│   ├── config/config.go        lectura de variables de entorno
│   ├── storage/
│   │   ├── db.go               apertura de SQLite, pragmas
│   │   └── migrate.go          esquema
│   ├── user/
│   │   ├── user.go             modelo + validación
│   │   └── store.go            CRUD sobre SQLite
│   ├── auth/
│   │   ├── password.go         bcrypt
│   │   ├── session.go          creación, lookup y expiración de sesiones
│   │   └── middleware.go       RequireAuth
│   ├── hls/
│   │   ├── pool.go             parseo del manifiesto + tabla acumulada
│   │   ├── snapshot.go         Snapshot inmutable + render del .m3u8
│   │   ├── engine.go           goroutine del reloj + atomic.Pointer
│   │   └── *_test.go
│   ├── viewers/
│   │   ├── hub.go              hub SSE
│   │   └── hub_test.go
│   └── web/
│       ├── router.go           rutas y middleware
│       ├── pages.go            register, login, logout, player
│       ├── stream.go           .m3u8 y .ts
│       ├── events.go           SSE
│       └── templates/*.html
├── web/static/                 hls.js vendorizado, app.css, app.js
├── segments/                   .ts + segment.m3u8 (fuera de Git)
├── docs/                       estos documentos
└── Dockerfile
```

**Regla de dependencias:** `web` depende de `hls`, `auth`, `viewers`. Ninguno de esos tres
depende de `web` ni conoce `net/http` como concepto de dominio (salvo `auth/middleware.go`,
que por definición es HTTP). En particular **`hls` no sabe que existe HTTP**: expone un
snapshot y un lector de segmentos, y nada más. Eso es lo que lo hace testeable sin levantar
un servidor.

## Flujo de datos del stream

```text
  [goroutine del reloj]                    [N handlers HTTP]
          │                                        │
  duerme hasta el instante exacto                  │
  de la próxima rotación                           │
          │                                        │
  construye Snapshot inmutable                     │
   {seq, ventana[3], m3u8 []byte}                  │
          │                                        │
   atomic.Store(ptr) ──────────►  atomic.Load(ptr) ─┘
                                        │
                                  w.Write(snap.m3u8)
```

Los lectores no bloquean ni al escritor ni entre sí. El trabajo por rotación es constante
e independiente del número de espectadores.

## Modelo de datos

```sql
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
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

En `sessions` se guarda el **SHA-256 del token**, no el token. Una filtración de la base
entrega hashes inservibles en vez de sesiones activas.

## Rutas

| Ruta | Auth | Qué hace |
| --- | --- | --- |
| `GET /` | — | Redirige a `/player` si hay sesión, si no a `/login` |
| `GET /register` · `POST /register` | — | Alta: nombre, email, contraseña |
| `GET /login` · `POST /login` | — | Inicio de sesión |
| `POST /logout` | ✔ | Borra la sesión y la cookie |
| `GET /player` | ✔ | Página del player |
| `GET /live/stream.m3u8` | ✔ | Playlist vivo, `Cache-Control: no-cache, no-store` |
| `GET /live/segments/{name}` | ✔ | Segmento `.ts`, cacheable e inmutable |
| `GET /live/events` | ✔ | SSE: espectadores + estado del stream |
| `GET /healthz` | — | Healthcheck de Docker |
| `GET /static/*` | — | Assets estáticos |

**El requisito 4 se cumple en las cuatro rutas marcadas, no sólo en `/player`.** Proteger
únicamente la página dejaría el `.m3u8` y los `.ts` accesibles sin cuenta, que es
precisamente lo que el requisito busca impedir.

## Manejo de memoria

Tres decisiones concretas, porque es un criterio explícito de evaluación:

1. **Los `.ts` nunca se cargan completos en RAM.** Se sirven con `http.ServeContent` sobre
   un `*os.File`, que copia en bloques y además habilita *range requests* sin código extra.
   Un segmento de 13 MB consume del orden de KB de RAM del servidor, no 13 MB.
2. **El `.m3u8` se renderiza una vez por rotación, no por request.** El costo por petición
   es un `Write` de un `[]byte` ya construido.
3. **El estado es acotado y constante:** un snapshot vigente (decenas de bytes) más la
   tabla de duraciones (64 valores). No crece con el número de espectadores.

## Manejo de errores

- **Arranque:** si falta la carpeta de segmentos o el manifiesto, el servidor **no levanta**
  e informa exactamente qué falta. Preferible a un servidor arriba sirviendo 404s.
- **Login:** mensaje genérico ("credenciales inválidas") tanto si el email no existe como si
  la contraseña es incorrecta, para no filtrar qué emails están registrados.
- **Registro:** email duplicado, formato inválido y contraseña corta con mensajes distintos.
- **Segmento inexistente:** 404 y log, sin tumbar el stream.
- **Cliente SSE lento:** canal con buffer acotado; si se llena, se descarta el mensaje en vez
  de bloquear el hub. Sin esto, un solo cliente lento congela el broadcast a todos y las
  goroutines se acumulan.
- **Desconexión:** se detecta con `r.Context().Done()`; el hub des-registra al cliente.
- **Apagado:** `http.Server.Shutdown` más cancelación por contexto del motor y del hub.

## Estrategia de tests

Concentrados donde está el riesgo: el motor HLS. Detalle en cada documento.

- Derivación de la secuencia, incluidos los bordes exactos de rotación.
- Avance correcto ante el segmento de duración distinta (4,566667s).
- Vuelta del ciclo: `MEDIA-SEQUENCE` sigue creciendo y la discontinuidad cae donde debe.
- Parseo del manifiesto provisto, ignorando `EXT-X-ENDLIST`.
- Auth: hash/verificación, expiración, rechazo sin cookie.

**Decisión que esto impone:** el reloj se inyecta como dependencia (`func() time.Time`).
Sin eso, probar la vuelta del ciclo exigiría esperar 10,5 minutos reales; con eso la suite
corre en milisegundos y es determinista.

## Configuración

Todo por variables de entorno, con valores por defecto que funcionan sin configurar nada.

| Variable | Default | Uso |
| --- | --- | --- |
| `PORT` | `8080` | Puerto HTTP |
| `DB_PATH` | `/data/zapping.db` | Archivo SQLite |
| `SEGMENTS_DIR` | `/app/segments` | Carpeta con los `.ts` y el manifiesto |
| `SESSION_TTL` | `24h` | Vigencia de la sesión |
| `SECURE_COOKIES` | `false` | `true` detrás de HTTPS |
| `WINDOW_SIZE` | `3` | Segmentos por playlist (el enunciado fija 3) |

## Fuera de alcance

Explícitamente no se hace, para mantener el alcance acordado:

- Múltiples calidades / master playlist (requeriría transcodificar; hay una sola versión).
- Recuperación de contraseña, verificación por email, roles.
- Persistencia de métricas históricas: el contador de espectadores es sólo en vivo.
- HTTPS: se asume detrás de un proxy en producción.
