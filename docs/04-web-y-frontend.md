# 04 — Web y frontend

Paquetes `internal/web` y `internal/viewers`, más `web/static` y los templates.
Cumple el requisito 2 (tres páginas), la parte B del enunciado y la feature opcional.

## Router

`net/http` con el routing nativo de Go 1.22+ (`mux.Handle("GET /ruta", h)`), sin framework.

```go
func NewRouter(deps Deps) http.Handler
```

Middleware encadenado a mano — son tres funciones que envuelven un `http.Handler`:
`logging` → `recover` → `auth` (sólo en las rutas protegidas).

## Handlers de páginas — `pages.go`

Las tres páginas del requisito 2, renderizadas en el servidor con `html/template`. El
escapado contextual de `html/template` cubre XSS sin trabajo extra.

| Handler | Ruta | Notas |
| --- | --- | --- |
| `handleRegister` | `GET`/`POST /register` | Nombre, email, contraseña |
| `handleLogin` | `GET`/`POST /login` | Email y contraseña |
| `handleLogout` | `POST /logout` | Sólo POST: un `GET` permitiría cerrar sesión ajena con un `<img>` |
| `handlePlayer` | `GET /player` | Protegida |
| `handleRoot` | `GET /` | Redirige según haya sesión |

En error de formulario se re-renderiza la misma página con el mensaje y **los campos ya
escritos conservados** (menos la contraseña). Perder lo tipeado en cada error es una
molestia evitable con dos líneas.

## Handlers del stream — `stream.go`

```go
// GET /live/stream.m3u8
snap := engine.Current()               // atomic.Load, wait-free
w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
w.Write(snap.Playlist)                 // bytes ya renderizados
```

El `no-cache` **no es opcional**: si el navegador o un proxy cachea el playlist, el player
recibe una ventana vieja, se sale del rango disponible y se corta.

```go
// GET /live/segments/{name}
// - valida el nombre contra el pool (rechaza path traversal)
// - http.ServeContent sobre el *os.File
w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
```

Los `.ts` sí se cachean agresivamente: su contenido nunca cambia. Y `ServeContent` copia por
bloques, así que un segmento de 13 MB no entra completo en RAM.

## `internal/viewers` — el hub SSE

Patrón de goroutine con dueño único: **una sola goroutine es dueña del conjunto de clientes**,
así que ese mapa no necesita lock alguno. Todo lo demás entra por canales.

```go
type Hub struct {
    join    chan *client
    leave   chan *client
    publish chan Event
    count   atomic.Int64      // lectura barata desde cualquier parte
}

func (h *Hub) Run(ctx context.Context)   // dueña única del set de clientes
func (h *Hub) Subscribe() (*client, func())
func (h *Hub) Publish(e Event)
func (h *Hub) Count() int64
```

**Backpressure — el punto crítico.** Cada cliente tiene un canal con buffer acotado
(capacidad 8). Al publicar:

```go
select {
case c.ch <- e:      // hay espacio
default:             // cliente lento: se descarta este evento
}
```

Sin ese `default`, un solo cliente lento bloquea el envío y **congela el broadcast para
todos**, mientras las goroutines se acumulan. Es la fuga de memoria clásica de este patrón
y es exactamente lo que el criterio de RAM del enunciado busca ver resuelto. Descartar un
evento no tiene costo real: el siguiente llega en un segundo y trae el estado completo.

### El endpoint

```go
// GET /live/events  (protegido)
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")

ch, unsubscribe := hub.Subscribe()
defer unsubscribe()

for {
    select {
    case e := <-ch:
        fmt.Fprintf(w, "data: %s\n\n", e.JSON())
        w.(http.Flusher).Flush()
    case <-r.Context().Done():   // el cliente cerró la pestaña
        return
    }
}
```

`r.Context().Done()` es lo que garantiza que no queden goroutines huérfanas cuando alguien
cierra el navegador.

### El evento

```json
{
  "viewers": 3,
  "sequence": 142,
  "window": ["segment14.ts", "segment15.ts", "segment16.ts"],
  "nextRotationMs": 4200,
  "discontinuity": false
}
```

Se publica cuando cambia el número de espectadores y en cada rotación del motor. El motor
notifica al hub llamando a un **hook síncrono** (`onRotate`, registrado vía
`hls.WithRotationHook`), no publicando en un canal propio de `hls` — ese paquete no expone
ningún canal ni conoce al hub. El hook corre en la propia goroutine de rotación del motor,
así que es el hub quien tiene que reenviar el snapshot a su canal `publish` interno **sin
bloquear** (ver el `select`/`default` de arriba); si el hook del hub bloqueara, bloquearía
también el avance del stream. `hls` sigue sin conocer ni HTTP ni al hub: sólo conoce la firma
`func(*Snapshot)` que le pasan.

## El player — `templates/player.html` + `static/app.js`

hls.js **vendorizado**, no por CDN. Si el evaluador levanta el contenedor sin internet, un
CDN rompe la entrega — y el enunciado pide "un docker con el aplicativo funcionando".

```js
if (Hls.isSupported()) {
  const hls = new Hls({
    liveSyncDurationCount: 3,   // se posiciona al inicio de la ventana: máximo margen
    lowLatencyMode: false,      // no aplica: no es LL-HLS
    enableWorker: true,         // demux fuera del hilo principal
  });
  hls.loadSource('/live/stream.m3u8');
  hls.attachMedia(video);
} else if (video.canPlayType('application/vnd.apple.mpegurl')) {
  video.src = '/live/stream.m3u8';   // Safari reproduce HLS nativo
}
```

`liveSyncDurationCount: 3` con una ventana de 3 posiciona al player al **comienzo** de la
ventana, que es la posición con más colchón disponible. Con una ventana del mínimo que
permite la spec de HLS, ese margen es todo lo que hay.

Manejo de errores de hls.js: los de red y de media se recuperan con
`startLoad()` / `recoverMediaError()`; un `401` lleva al usuario a `/login`.

## Panel en vivo

```text
┌─────────────────────────────┬──────────────────────┐
│                             │  ● EN VIVO           │
│                             │  👁  3 espectadores  │
│         [ video ]           │                      │
│                             │  SEQUENCE   142      │
│                             │  ventana             │
│                             │  ┌────┬────┬────┐    │
│                             │  │ 14 │ 15 │ 16 │    │
│                             │  └────┴────┴────┘    │
│                             │  próx. rotación  4s  │
└─────────────────────────────┴──────────────────────┘
```

Cumple dos funciones: es el "detalle entretenido" que el enunciado invita, y **le muestra al
evaluador en tiempo real la mecánica que está calificando** — el `MEDIA-SEQUENCE` subiendo,
los segmentos desplazándose, y la rotación corta cuando le toca al segmento de 4,57s.

El contador de rotación se anima en el cliente entre eventos (`requestAnimationFrame`), no
pidiendo al servidor. El SSE sólo corrige la referencia.

## Estilo — glassmorfismo, sin Bootstrap

CSS propio, ~150 líneas. Bootstrap se descarta porque habría que sobrescribir sus
componentes para quitarles el aspecto por defecto: más trabajo que escribirlo, y menos
legible. hls.js queda como única dependencia de frontend.

```css
:root {
  --glass-bg:     rgba(255, 255, 255, .06);
  --glass-border: rgba(255, 255, 255, .12);
  --accent:       #e50914;
}

.glass {
  background: var(--glass-bg);
  backdrop-filter: blur(14px) saturate(140%);
  -webkit-backdrop-filter: blur(14px) saturate(140%);
  border: 1px solid var(--glass-border);
  border-radius: 16px;
}
```

**Regla estricta: vidrio al lado del video, nunca encima.** `backdrop-filter` recalcula el
desenfoque cada vez que cambia lo que hay detrás; sobre un video en reproducción eso son
25-30 recálculos en GPU por segundo, y en una máquina modesta puede tirar frames del propio
video. Para cualquier overlay sobre el `<video>` (controles, botón de "volver al vivo") se
usa un degradado semitransparente **sin** `backdrop-filter`: se ve casi igual y cuesta ~0.

Contraste de texto ≥ 4.5:1 sobre el fondo translúcido, que es donde este estilo suele fallar.
Se verifica en las tres páginas.

Detalles: punto rojo pulsante en el indicador de LIVE, las celdas de la ventana se animan al
desplazarse, y un botón "volver al vivo" si el usuario pausa y queda atrás.

## Tests

Los handlers se prueban con `net/http/httptest`, sin levantar un servidor real.

| Test | Qué verifica |
| --- | --- |
| `TestPlaylistHeaders` | `Content-Type` correcto y `no-cache` presente |
| `TestPlaylistSinAuth` | 401, no 302 |
| `TestSegmentoTraversal` | `../../etc/passwd` → 400/404, nunca sirve el archivo |
| `TestSegmentoHeaders` | Cacheable e inmutable |
| `TestRegistroDuplicado` | Re-renderiza con mensaje, conserva nombre y email, no 500 |
| `TestLogoutSoloPost` | `GET /logout` → 405 |
| `TestHubCuentaEspectadores` | Alta y baja mueven el contador |
| `TestHubClienteLento` | Un cliente con el buffer lleno no bloquea a los demás |
| `TestHubDesconexion` | Cancelar el contexto des-registra y no deja goroutines |

`TestHubClienteLento` es el que respalda la afirmación sobre manejo de RAM: conviene que
exista y que se note.

## Criterios de aceptación

- [ ] Las tres páginas del requisito 2 existen y funcionan.
- [ ] El video se reproduce de forma continua, incluida la vuelta del ciclo.
- [ ] El panel muestra espectadores y `MEDIA-SEQUENCE` en tiempo real.
- [ ] Abrir dos pestañas sube el contador a 2; cerrar una lo baja a 1.
- [ ] Ningún elemento con `backdrop-filter` se superpone al `<video>`.
- [ ] Sin peticiones a dominios externos: funciona con el contenedor aislado de internet.
- [ ] Cerrar la pestaña no deja goroutines vivas (verificable con `pprof`).
