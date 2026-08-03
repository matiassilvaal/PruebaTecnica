# 04 — Web y frontend

Paquetes `internal/web`, `internal/viewers` e `internal/config`, más `internal/web/static`
y `internal/web/templates`, y el cableado en `cmd/server`.
Cumple el requisito 2 (tres páginas), la parte B del enunciado y la feature opcional.

> **Estado:** implementado. Este documento se reconcilió con el código al cerrar el bloque;
> lo que dice es lo que hay.

## Router

`net/http` con el routing nativo de Go 1.22+ (`mux.Handle("GET /ruta", h)`), sin framework.

```go
func NewRouter(d Deps) http.Handler
```

Middleware encadenado a mano: `registrar` → `recuperar` → mux, y `Guard.RequirePage` /
`Guard.RequireAPI` colgados de cada ruta protegida. El orden de los dos primeros no es
arbitrario: registrar va **por fuera** para que la línea de log exista también cuando el
handler entra en pánico y `recuperar` lo atrapa.

Dos detalles del routing nativo que hacen trabajo real sin escribir código:

- `GET /{$}` casa la raíz **exacta**. Sin el `{$}`, `GET /` sería el comodín que atrapa
  cualquier ruta no registrada, y toda URL inexistente terminaría redirigiendo al login en
  vez de dar 404.
- Registrar **sólo** `POST /logout` hace que un `GET /logout` devuelva 405 solo. Con un GET
  aceptado, un `<img src="/logout">` en cualquier página cerraría la sesión del visitante.

## Handlers de páginas — `pages.go`

Las tres páginas del requisito 2, renderizadas en el servidor con `html/template`. El
escapado contextual de `html/template` cubre XSS sin trabajo extra.

| Handler | Ruta | Notas |
| --- | --- | --- |
| `registroForm` / `registroEnviar` | `GET`/`POST /register` | Nombre, email, contraseña |
| `loginForm` / `loginEnviar` | `GET`/`POST /login` | Email y contraseña |
| `logout` | `POST /logout` | Sólo POST: un `GET` permitiría cerrar sesión ajena con un `<img>` |
| `player` | `GET /player` | Protegida |
| `raiz` | `GET /{$}` | Redirige según haya sesión |

En error de formulario se re-renderiza la misma página con el mensaje y **los campos ya
escritos conservados** (menos la contraseña). Perder lo tipeado en cada error es una
molestia evitable con dos líneas.

El código de esas re-renderizaciones es **422 (Unprocessable Content), no 200**. Un 200
diría que la petición se procesó correctamente y no es cierto; 422 es exacto y el navegador
pinta el cuerpo igual. Las credenciales incorrectas van con 401, y sólo un fallo de la base
da 500.

### Las cuatro restricciones que el bloque 03 dejó anotadas

Los handlers son el primer llamante de casi todo lo que se construyó en `internal/auth` e
`internal/cuenta`, así que acá es donde esas restricciones se cumplen o se pierden:

1. **El alta va por `cuenta.Registrar`, nunca por `Store.Crear`.** `Crear` recibe el hash ya
   calculado y no valida nada: llamarlo desde el handler permitiría guardar la contraseña en
   claro y saltarse las reglas de alta. `Registrar` recibe `auth.HashPassword` como
   parámetro y cierra ese hueco.
2. **`auth.VerificarEnVacio()` en la rama del email inexistente.** Sin esa llamada, un email
   que no existe responde en microsegundos mientras que uno registrado paga los ~370 ms de
   bcrypt, y esa diferencia convierte el login en un verificador de qué cuentas existen
   aunque el mensaje de error sea idéntico. Este bloque es su **primer llamante**: la
   función estaba implementada y probada desde el 03, y hasta acá no la usaba nadie.
3. **`Sessions.DestruirDeUsuario` antes de `Crear`** en el login. Es la rotación de sesión
   contra session fixation: un token que un atacante haya conseguido fijar en el navegador
   de la víctima deja de valer en cuanto el login tiene éxito.
4. **La cookie se emite con `Guard.PonerCookie(w, token)`**, que toma el TTL de `Sessions`.
   Una sola fuente de verdad, para que la cookie y la fila de la base no caduquen en
   momentos distintos.

Además, `render` responde con `Cache-Control: no-store`. El caso que lo justifica es
**`/player`**: responde 200, lleva el nombre del usuario, y un 200 sin `Set-Cookie` y sin
directivas de caché es cacheable **heurísticamente** por un intermediario. Los 422 de los
formularios nunca lo fueron —422 no está entre los códigos que la RFC 9110 §15 permite
cachear así—, pero la cabecera se emite para todas por igual: una sola regla es más fácil de
sostener que una excepción por código.

## Handlers del stream — `stream.go`

```go
// GET /live/stream.m3u8
snap := s.motor.Current()              // atomic.Load, wait-free
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
w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
```

Los `.ts` sí se cachean agresivamente: su contenido nunca cambia. Pero `private`, no `public`:
la ruta va detrás de `RequireAPI`, y una cookie de sesión no inhibe por sí sola el
almacenamiento compartido (eso sólo lo hace un `Authorization`). Con `public` cualquier caché
intermedia quedaría autorizada a guardar un segmento autenticado y servírselo durante un año a
quien no tiene cuenta. El ahorro no se pierde, porque viene de la caché del navegador.

Y `ServeContent` copia por bloques, así que un segmento de 13 MB no entra completo en RAM. El
envoltorio de logging reexpone `io.ReaderFrom` además de `http.Flusher` justamente para no
tapar el camino de copia cero que `ServeContent` elige cuando el writer lo ofrece.

## `internal/viewers` — el hub SSE

Patrón de goroutine con dueño único: **una sola goroutine es dueña del conjunto de clientes**,
así que ese mapa no necesita lock alguno. Todo lo demás entra por canales.

```go
type Hub struct {
    alta      chan *cliente
    baja      chan *cliente
    difundir  chan Evento
    terminado chan struct{}   // se cierra cuando Run vuelve

    espectadores atomic.Int64  // lectura barata desde cualquier parte
}

func NewHub() *Hub
func (h *Hub) Run(ctx context.Context)              // dueña única del set de clientes
func (h *Hub) Suscribir() (<-chan Evento, func())   // canal de sólo lectura + baja idempotente
func (h *Hub) Publicar(e Evento)                    // NUNCA bloquea
func (h *Hub) Espectadores() int64
```

`Suscribir` devuelve un canal **de sólo lectura** —quien escribe es siempre la goroutine
dueña— y una función de baja **idempotente**, segura de llamar aunque el hub ya se haya
apagado. Y es **síncrona respecto del alta**: cuando vuelve, el cliente ya está en el
conjunto, `Espectadores()` ya lo cuenta y el estado vigente ya está en su canal. Esa
sincronía cuesta un viaje de ida y vuelta por suscripción —una vez por espectador, no por
rotación— y a cambio elimina toda una clase de carreras entre quien se suscribe y la
goroutine dueña.

### Backpressure: hay dos lugares donde no se puede bloquear, no uno

El diseño original contemplaba uno solo. En la implementación resultaron ser **dos**, y por
razones distintas:

**1. El envío a cada cliente.** Cada uno tiene un canal con buffer acotado (capacidad 8):

```go
select {
case c.ch <- e:      // hay espacio
default:             // cliente lento: se descarta este evento
}
```

Sin ese `default`, un solo cliente lento bloquea a la goroutine dueña y con ella **congela
el broadcast para todos**, mientras los eventos se acumulan sin techo. Es la fuga de memoria
clásica de este patrón y es exactamente lo que el criterio de RAM del enunciado busca ver
resuelto.

**2. `Publicar` mismo.** Lo llama el hook de rotación del motor, que corre **síncronamente
en la goroutine que hace avanzar el stream**. Si `Publicar` se bloqueara con el canal de
difusión lleno, el stream se detendría **para todos los espectadores**, no sólo para el
panel. Por eso `Publicar` lleva su propio `select`/`default` y su contrato es literalmente
"NUNCA BLOQUEA".

Descartar es correcto en los dos casos porque cada evento trae el **estado completo, no un
delta**: el siguiente pone al día igual.

### El endpoint

```go
// GET /live/events  (protegido)
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")
w.Header().Set("X-Accel-Buffering", "no")
w.WriteHeader(http.StatusOK)
vaciar.Flush()                           // manda las cabeceras YA, antes de Suscribir

eventos, salir := hub.Suscribir()
defer salir()

latido := time.NewTicker(20 * time.Second)
defer latido.Stop()

for {
    select {
    case e, abierto := <-eventos:
        if !abierto {
            return                       // el hub se apagó: cierre ordenado
        }
        w.Write(append(append([]byte("data: "), cuerpo...), '\n', '\n'))
        vaciar.Flush()
    case <-latido.C:
        w.Write([]byte(": latido\n\n"))
        vaciar.Flush()
    case <-r.Context().Done():           // el cliente cerró la pestaña
        return
    }
}
```

Cuatro cosas que no estaban en el diseño y que la implementación necesitó:

- **El `WriteHeader` + `Flush` antes de `Suscribir`.** Hoy no cambia nada observable, porque
  el hub siempre deja un evento listo antes de que `Suscribir` vuelva. Existe para que el
  tiempo hasta el primer byte dependa de este handler y no de cuánto tarde un paquete
  distinto: heredar esa garantía en silencio la volvería frágil ante cualquier cambio en
  `viewers`.

- **`X-Accel-Buffering: no`.** nginx bufferiza las respuestas por defecto, y eso convierte un
  stream de eventos en una entrega a bloques: el panel se movería a saltos o no se movería
  nunca. No cuesta nada dejarlo puesto y evita un síntoma que sólo aparecería detrás de un
  proxy, es decir en producción y no en los tests.
- **Un latido cada 20 s.** Entre rotaciones pueden pasar 10 segundos sin un solo byte, y un
  proxy inverso con timeout de inactividad corta ahí. El comentario SSE (`: latido`) no llega
  a `EventSource` —el navegador descarta los comentarios— pero sí cuenta como tráfico para el
  proxy.
- **La línea en blanco que cierra el mensaje.** El terminador de SSE es `\n\n`, no `\n`. Con
  uno solo el navegador espera más datos y **no despacha nada**: el panel quedaría muerto sin
  que ningún test fallara ni apareciera una línea de log. Hay un test que fija exactamente
  eso (`TestSSEElMensajeTerminaEnLineaEnBlanco`).

El canal cerrado se distingue del evento (`e, abierto := <-eventos`) porque el apagado del
hub cierra los canales de sus clientes: es lo que hace que los handlers SSE vuelvan solos
cuando el proceso se apaga, en vez de quedar esperando eventos que no van a llegar.

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

Tres cosas las pone el hub, no el llamante:

- **`viewers`.** El hook de rotación no sabe cuántos espectadores hay; el hub es el único que
  lo sabe, así que completa ese campo al difundir. Por eso `HookDeRotacion` publica el evento
  con `Espectadores` en cero y llega al panel con el número correcto.
- **`nextRotationMs`.** El hook publica el **instante** de la próxima rotación (`ProximaEn`,
  fuera del JSON) y el hub deriva los milisegundos **al enviar**. Tiene que ser así porque el
  hub reenvía el último evento en cada alta y cada baja: con el plazo calculado al publicar,
  abrir una segunda pestaña le mandaba a todos los espectadores la cuenta del momento de la
  rotación, y en el navegador el contador saltaba hacia atrás —de "3.0 s" a "10.0 s"— con la
  barra de progreso de vuelta a cero. Calculado en el envío, cada destinatario recibe el
  plazo relativo al instante en que se le mandó a él.
- **El último evento se recuerda.** `Run` guarda el estado más reciente y se lo entrega al
  instante a quien se conecta. Sin eso el panel quedaría vacío hasta la próxima rotación —
  hasta 10 segundos mirando ceros—, y además sirve para reconstruir el evento cuando lo único
  que cambió fue el número de espectadores. La contrapartida es que el slice `Ventana` sigue
  vivo mucho después de la llamada a `Publicar` y lo comparten todos los espectadores: es de
  **sólo lectura**, y por eso el hook copia los nombres en vez de pasar `Snapshot.Window`.

Se publica cuando cambia el número de espectadores y en cada rotación del motor. El motor
notifica al hub llamando a un **hook síncrono** (`onRotate`, registrado vía
`hls.WithRotationHook`), no publicando en un canal propio de `hls` — ese paquete no expone
ningún canal ni conoce al hub. El hook corre en la propia goroutine de rotación del motor,
así que es el hub quien tiene que reenviar el snapshot a su canal `publish` interno **sin
bloquear** (ver el `select`/`default` de arriba); si el hook del hub bloqueara, bloquearía
también el avance del stream. `hls` sigue sin conocer ni HTTP ni al hub: sólo conoce la firma
`func(*Snapshot)` que le pasan.

## El player — `internal/web/templates/player.html` + `internal/web/static/player.js`

**Por qué los assets viven bajo `internal/web/` y no en un `web/` de la raíz:** van embebidos
con `go:embed`, y `embed` no puede subir de directorio — un `//go:embed ../../web/static` no
compila. Beneficio adicional, y es el que decide: el binario queda autosuficiente, el
Dockerfile **no necesita `COPY web/`** y un `go run ./cmd/server` funciona desde cualquier
directorio de trabajo.

hls.js **vendorizado**, no por CDN. Si el evaluador levanta el contenedor sin internet, un
CDN rompe la entrega — y el enunciado pide "un docker con el aplicativo funcionando". Hay un
test que recorre el CSS, el JS y las plantillas buscando referencias a dominios externos
(`TestElFrontendNoPideNadaAInternet`), porque esto es de las cosas que se rompen al agregar
una fuente "sólo para probar".

Las URL del stream **no están en el JS**: llegan desde el HTML que renderiza el servidor
(`data-playlist`, `data-eventos` en `.escenario`), que es quien conoce su propio árbol de
rutas. Repetirlas en el JS crearía una segunda fuente de verdad que puede divergir de la
primera sin que nada se queje, y los tests de Go no ejecutan JavaScript.

```js
var escenario = document.querySelector('.escenario');
var urlPlaylist = escenario.dataset.playlist;   // la sirve el servidor, no el JS

// window.Hls y no Hls a secas: si el script vendorizado no cargó, `Hls` no está
// definido y evaluarlo lanzaría en vez de caer al camino nativo de Safari.
if (window.Hls && window.Hls.isSupported()) {
  var hls = new Hls({
    liveSyncDurationCount: 3,   // se posiciona al inicio de la ventana: máximo margen
    lowLatencyMode: false,      // no aplica: no es LL-HLS
    enableWorker: true,         // demux fuera del hilo principal
  });
  hls.loadSource(urlPlaylist);
  hls.attachMedia(video);
} else if (video.canPlayType('application/vnd.apple.mpegurl')) {
  video.src = urlPlaylist;      // Safari reproduce HLS nativo
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

`internal/web/static/app.css`, embebido igual que el resto por la misma razón. CSS propio.
Bootstrap se descarta porque habría que sobrescribir sus
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

Esa regla **está verificada por un test** (`TestNingunVidrioSeSuperponeAlVideo`), no confiada
a la memoria de quien retoque el CSS: el test parsea `app.css` y falla si `video`, `.marco`,
`.superpuesto` o `.sobre-video` aparecen en un selector cuya regla usa `backdrop-filter`.
Vale anotar que su primera versión no detectaba nada — la expresión que extraía el selector
se comía el comentario que precedía a la regla, y con eso el nombre nunca coincidía. Se
descubrió mutando el CSS a propósito.

Contraste de texto ≥ 4.5:1 sobre el fondo translúcido, que es donde este estilo suele fallar.
Se verifica en las tres páginas.

Detalles: punto rojo pulsante en el indicador de LIVE, las celdas de la ventana se animan al
desplazarse, y un botón "volver al vivo" si el usuario pausa y queda atrás.

## `cmd/server` — el cableado

Es el **único** lugar donde los paquetes se conocen entre sí: cada uno recibe sus
dependencias ya construidas y ninguno busca nada por su cuenta. Eso es lo que permite probar
`web` sin levantar el proceso y `viewers` sin motor ni HTTP.

Tres goroutines, todas atadas al mismo contexto raíz: `hub.Run`, `motor.Run` y
`limpiarSesiones`. `signal.NotifyContext` ata SIGINT y SIGTERM a ese contexto, así que
`docker stop` y un Ctrl+C recorren exactamente el mismo camino.

El orden del apagado importa y está al revés de lo que parece natural: **primero se cancela
el contexto, después `srv.Shutdown`**. Si `Shutdown` fuera primero, esperaría su plazo
completo a que se cerraran unas conexiones SSE diseñadas para no cerrarse nunca; cancelando
antes, el hub cierra los canales de sus clientes, los handlers SSE vuelven solos y `Shutdown`
termina en milisegundos.

Lo que la cancelación **no** da es una precedencia: cerrar los canales del hub es una carrera
con `Shutdown`, no un paso anterior. Es benigna en las dos direcciones — `Shutdown` bloquea
esperando a las conexiones vivas y el hub las libera en microsegundos (el apagado completo se
midió en ~586 ms), y si el hub quedara sin CPU el peor caso es agotar `plazoApagado`, no una
respuesta a medias.

`plazoApagado` son **8 s y no 10** para que quepa dentro del plazo de `docker stop`, que manda
SIGKILL a los 10 s del SIGTERM: con los dos números iguales no hay margen y una sola conexión
trabada convertiría un apagado limpio en un contenedor matado a la fuerza.

Dos cosas que el archivo declara como decisión y no como olvido:

- **Sin `WriteTimeout`** en el `http.Server` (ver `docs/06-decisiones.md`), pero **con
  `IdleTimeout`** de 120 s: ese sólo corre entre peticiones de una conexión keep-alive y no
  puede tocar un SSE en curso. Un test fija las dos cosas a la vez, para que nadie "arregle"
  la ausencia de `WriteTimeout` al agregar el otro.
- **Las tres goroutines no se esperan** antes del `defer db.Close()`. Las tres salen por
  `ctx.Done()` casi al instante, la única que toca la base registra el error y sigue, y
  SQLite hace commit o no lo hace. Esperarlas costaría un `WaitGroup` y tres cierres
  coordinados para cubrir un riesgo que no existe.

Si no hay segmentos, **no levanta**: un servidor arriba sirviendo 404 parece sano —responde,
el healthcheck pasa— y el problema aparecería recién cuando alguien intente ver el stream.

## Tests

**69 tests** en los cuatro paquetes del bloque: `internal/config` (3), `internal/viewers` (11),
`internal/web` (51) y `cmd/server` (4). Los handlers se prueban con `net/http/httptest`, sin
levantar un servidor real; el hub se prueba sin HTTP y sin motor, que es lo que compra su
ignorancia deliberada de los dos.

Los que sostienen una afirmación del documento, no los 66:

| Test | Qué verifica |
| --- | --- |
| `TestPlaylistCabeceras` | `Content-Type` de HLS y `no-cache, no-store` presentes |
| `TestPlaylistEsElDelSnapshot` | El cuerpo es byte a byte el playlist del snapshot, con URI relativas a `segments/` |
| `TestPlaylistNoMutaElSnapshotCompartido` | Dos peticiones seguidas no tocan el array compartido |
| `TestPlaylistSinSesionEs401` | 401 y no 302: con un redirect, hls.js parsearía la página de login como playlist |
| `TestSegmentoTraversal` | `../` y `%2e%2e%2f` nunca sirven un archivo fuera del pool (404, o 301 si el mux limpió la ruta) |
| `TestSegmentoSoportaRangos` | 206 y el rango pedido: es lo que `ServeContent` da y `io.Copy` no |
| `TestSegmentoSirveElArchivo` | `video/mp2t`, `private`, `max-age=31536000`, `immutable`, y nunca `public` detrás de la sesión |
| `TestRegistroCreaLaCuentaYDejaSesionIniciada` | El alta pasa por `cuenta.Registrar`: la contraseña queda hasheada, no en claro |
| `TestRegistroInvalidoConservaLoTipeado` | 422 con nombre y email de vuelta, sin la contraseña |
| `TestRegistroEscapaElHTML` | El escapado contextual de `html/template` cubre XSS |
| `TestLoginRotaLaSesion` | `DestruirDeUsuario` antes de `Crear`: el token viejo deja de valer |
| `TestLoginConEmailInexistentePagaBcrypt` | `auth.VerificarEnVacio()` empareja el tiempo de respuesta |
| `TestLoginMalNoDistingueSiLaCuentaExiste` | Mismo código y mismo mensaje en las dos ramas |
| `TestLogoutSoloAceptaPost` | `GET /logout` → 405 |
| `TestPlayerExigeSesionYMuestraAlUsuario` | Protegida, y el nombre sale del contexto que dejó `RequirePage` |
| `TestSSEElMensajeTerminaEnLineaEnBlanco` | El terminador es `\n\n`: con `\n`, `EventSource` no despacha nada |
| `TestSSECabecerasYPrimerEvento` | `text/event-stream` y estado vigente al instante, sin esperar la rotación |
| `TestSSECuentaDosPestanas` | Dos conexiones → 2; cerrar una → 1 |
| `TestSSEDesconexionDesRegistra` | Cerrar la pestaña da de baja al cliente y no deja goroutines |
| `TestSSEElApagadoDelHubCierraLaConexion` | El handler vuelve solo cuando el hub se apaga |
| `TestSSENoFiltraDatosDelUsuario` | El evento no lleva nombre, email ni id |
| `TestHubClienteLentoNoBloqueaALosDemas` | Un cliente con el buffer lleno no congela el broadcast |
| `TestPublicarNoBloqueaConElHubDetenido` | `Publicar` vuelve aunque nadie esté leyendo: la garantía en la que se apoya todo el diseño |
| `TestHookDeRotacionNoBloqueaConElHubDetenido` | Lo mismo desde el lado del motor: el hook no detiene la rotación |
| `TestHubDesconexionNoDejaGoroutines` | Cancelar el contexto des-registra y no deja goroutines |
| `TestHubEntregaElUltimoEstadoAlConectar` | El hub recuerda el último evento y lo entrega al suscribirse |
| `TestElReenvioNoEntregaUnaCuentaRegresivaVieja` | El reenvío por alta/baja recalcula `nextRotationMs`: sin esto el contador del panel saltaba hacia atrás al abrir una segunda pestaña |
| `TestSinRotacionLaCuentaRegresivaEsCero` | Sin rotación previa el plazo es 0, no un negativo enorme derivado de un `time.Time` cero |
| `TestNingunVidrioSeSuperponeAlVideo` | Ningún selector con `backdrop-filter` toca el `<video>` |
| `TestElFrontendNoPideNadaAInternet` | Ni CSS, ni JS, ni plantillas referencian dominios externos |
| `TestElJsNoHardcodeaLasRutasDelStream` | Las URL del stream salen del HTML, no del JS |
| `TestElServidorNoLlevaWriteTimeout` | Fija la ausencia de `WriteTimeout` y la presencia de `IdleTimeout`, para que no se "arreglen" juntos |
| `TestRespuestaObservadaConservaElFlusher` | El middleware de log no rompe la aserción a `http.Flusher` del SSE |
| `TestRespuestaObservadaConservaElReaderFrom` | Ni la de `io.ReaderFrom`, que es la que le deja a `ServeContent` la copia cero del `.ts` |
| `TestCargarDefaults` | Los defaults se afirman con el entorno LIMPIO: un shell (o una imagen) con `PORT` puesto no puede decidir el resultado |

`TestHubClienteLentoNoBloqueaALosDemas` y `TestPublicarNoBloqueaConElHubDetenido` son los que
respaldan la afirmación sobre manejo de RAM y sobre sync/async: conviene que existan y que se
noten. El segundo no existía al principio — la garantía más importante del hub no tenía test
aislado, y eso se descubrió mutando el `select` de `Publicar` y viendo que nadie se quejaba.

Cobertura de los paquetes del bloque: `viewers` 95,8 %, `config` 93,9 %, `web` 77,0 %,
`cmd/server` 21,4 % (`run()` no se prueba: es cableado, y probarlo exigiría levantar el
proceso — lo que sí se verifica de ese archivo es `limpiarSesiones` y la ausencia de
`WriteTimeout`).

## Criterios de aceptación

Verificados por test automatizado:

- [x] Sólo usuarios registrados acceden a `/player`, `/live/stream.m3u8`, `/live/segments/*`
      y `/live/events`.
- [x] Ningún elemento con `backdrop-filter` se superpone al `<video>`.
- [x] Sin peticiones a dominios externos: funciona con el contenedor aislado de internet.
- [x] Cerrar la pestaña no deja goroutines vivas.
- [x] Dos conexiones SSE suben el contador a 2; cerrar una lo baja a 1.

Verificados a mano contra el proceso real (`go run ./cmd/server` con los 64 segmentos, por
`curl`):

- [x] Las tres páginas del requisito 2 existen y responden.
- [x] Registro → cookie de sesión → `.m3u8` autenticado, de punta a punta.
- [x] `/healthz` 200, `/live/stream.m3u8` sin sesión 401, `/` sin sesión 302 a `/login`.

Verificados en navegador, que es lo único que los prueba de verdad:

- [x] El video se reproduce de forma continua, incluida la vuelta del ciclo y el segmento de
      4,57 s.
- [x] El panel muestra espectadores y `MEDIA-SEQUENCE` en tiempo real.

Los dos últimos requieren un `<video>` de verdad decodificando: ningún test de Go los puede
sostener, y decir lo contrario sería justamente el tipo de afirmación sin respaldo que este
documento trata de no hacer.
