# zapping-live

Livestreaming HLS simulado a partir de 64 segmentos `.ts` pregrabados, con registro de
usuarios y un reproductor web al que sólo entran cuentas registradas. Todo en Go, entregado
como una imagen Docker.

Prueba técnica para Zapping. El enunciado está en [Prueba.md](Prueba.md).

---

## Levantarlo

Tres comandos. Los segmentos no van en el repositorio —son ~480 MB— así que el primero los
copia desde la carpeta que ustedes mismos proveyeron.

```bash
./scripts/prepare-segments.sh "hls test"
docker build -t zapping-live .
docker run -p 8080:8080 -v zapping-data:/data zapping-live
```

En Windows sin Git Bash, el primer comando es:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\prepare-segments.ps1 "hls test"
```

`-File` es necesario: sin él PowerShell interpreta el resto como `-Command`, vuelve a partir
la línea y `"hls test"` llega como dos argumentos. Y `-ExecutionPolicy Bypass` porque Windows
10 bloquea los `.ps1` por defecto.

Después, `http://localhost:8080` → crear cuenta → el player.

> El volumen `zapping-data` es un **volumen con nombre**, no una carpeta del host. Un bind
> mount (`-v ./data:/data`) no funciona sin ajustar permisos: el contenedor corre como uid
> 10001 y no podría escribir en un directorio creado por el host.

> **En Git Bash (Windows)** hay que anteponer `MSYS_NO_PATHCONV=1` al `docker run`, o Git
> Bash reescribe `-v zapping-data:/data` como `-v zapping-data:C:/Program Files/Git/data` y
> el contenedor arranca con la base en el lugar equivocado. En PowerShell, CMD, Linux y macOS
> no hace falta.

`docker build` produce una imagen para la arquitectura de la máquina que la construye. Para
otra —un Mac con Apple Silicon, por ejemplo— se pide explícitamente:

```bash
docker buildx build --platform linux/arm64 -t zapping-live --load .
```

La etapa de compilación corre siempre en la arquitectura del que construye y cross-compila
hacia la de destino (`CGO_ENABLED=0` lo hace trivial en Go), así que armar la imagen arm64
desde una máquina x86 tarda lo mismo que la nativa. Dejar que Docker emule el compilador con
QEMU tardaría diez veces más sin ganar nada.

El primer comando verifica **contra el manifiesto** que estén todos los segmentos que el
`.m3u8` nombra, y falla listando los que falten. Un conteo fijo daría por buena una copia a
la que le falta justo el archivo que hace falta, y el error aparecería recién dentro del
contenedor.

### Verificar que quedó bien

```bash
./scripts/smoke.sh
```

Levanta la imagen, corre 18 comprobaciones por HTTP —incluido que el stream dé 401 sin
sesión, que la ventana rote, y que `docker stop` apague ordenadamente— y apaga. Al final
lista lo que hay que mirar a mano en el navegador, que es lo que ningún script puede ver:
reproducción continua durante 12 minutos, el contador con dos pestañas, y que no salga
ninguna petición a un dominio externo.

### Variables de entorno

Todas tienen default. El contenedor funciona sin configurar nada.

| Variable | Default | Uso |
| --- | --- | --- |
| `PORT` | `8080` | Puerto HTTP |
| `DB_PATH` | `/data/zapping.db` | Archivo SQLite |
| `SEGMENTS_DIR` | `/app/segments` | Carpeta con los `.ts` y el manifiesto |
| `SESSION_TTL` | `24h` | Vigencia de la sesión |
| `SECURE_COOKIES` | `false` | `true` detrás de HTTPS |
| `WINDOW_SIZE` | `3` | Segmentos por playlist (el enunciado lo fija en 3) |

Una variable **ausente** cae al default; una **presente pero ilegible** aborta el arranque.
Caer al default ante un valor mal escrito dejaría al operador convencido de haber
configurado algo que nunca se aplicó.

---

## Arquitectura

Un binario, un contenedor, sin servicios externos.

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

Una goroutine hace avanzar la ventana del stream y publica un `Snapshot` inmutable; los
handlers HTTP lo leen con un `atomic.Load` sin tomar locks. Otra goroutine es dueña única
del conjunto de espectadores conectados y les difunde el estado por SSE. Una tercera barre
las sesiones vencidas. `hls` no sabe que existe HTTP y `viewers` no sabe que existe `hls`.

---

## Decisiones que vale la pena explicar

El detalle completo está en [docs/06-decisiones.md](docs/06-decisiones.md). Éstas son las
que más cambiaron el resultado.

### El reloj lo marca el tiempo de medios, no un ticker fijo

`segment63.ts` dura **4,566667 s**, no 10. Con un `time.Ticker` de 10 s, el player consumiría
ese segmento en 4,57 s mientras la ventana avanzaría recién a los 10: se adelantaría 5,43 s
por ciclo, se saldría del rango disponible y **la reproducción se cortaría**.

En vez de eso hay una tabla de sumas acumuladas de duraciones, y la ventana rota exactamente
cuando el segmento que sale terminó de reproducirse. De regalo, el motor soporta segmentos de
cualquier duración sin cambios, así que no hubo que descartar ningún archivo del material.

### La secuencia se deriva del reloj, no se incrementa

Con `seq++` los errores de temporización se acumulan. Derivando `seq` de `time.Since(inicio)`
contra la tabla acumulada, el valor es correcto siempre, aunque el proceso se congele o un
timer se atrase. La deriva no queda pequeña: queda **estructuralmente imposible**.

### Estado inmutable publicado de forma atómica

La goroutine del reloj construye un `Snapshot` nuevo y lo publica con `atomic.Pointer.Store`;
los lectores hacen `Load`. Nada se muta nunca. Eso elimina el estado mutable compartido, la
contención de lock, y deja las lecturas *wait-free*: un espectador o diez mil cuestan lo
mismo.

### Dos puntos de backpressure, por razones distintas

El hub descarta eventos en dos lugares, y no es el mismo problema:

1. **El envío a cada cliente.** Un espectador que dejó de leer bloquearía el broadcast para
   todos y acumularía eventos sin techo. Es la fuga de memoria clásica de este patrón.
2. **`Hub.Publicar`.** Lo llama el hook de rotación del motor, **síncronamente en la
   goroutine que hace avanzar el stream**. Si se bloqueara, el stream se detendría para
   todos los espectadores.

Descartar es correcto porque cada evento trae el estado completo, no un delta: el siguiente
pone al día igual.

### Manejo de memoria

Tres decisiones concretas, porque es un criterio explícito de evaluación:

- Los `.ts` **nunca se cargan completos en RAM**: se sirven con `http.ServeContent` sobre un
  `*os.File`, que copia por bloques y habilita *range requests* sin código extra. Un segmento
  de 13 MB cuesta del orden de KB de RAM del servidor.
- El `.m3u8` se renderiza **una vez por rotación**, no por request. Cada petición se reduce a
  escribir bytes ya listos.
- El estado por espectador está **acotado**: 8 eventos como techo duro, con descarte.

### El binario es autosuficiente

Plantillas, CSS, JavaScript y hls.js viajan **dentro** del binario con `go:embed`. El
contenedor no depende del directorio de trabajo y el `Dockerfile` no copia ningún asset.

### hls.js vendorizado, nunca por CDN

El enunciado pide un docker **funcionando**. Un `<script src="https://cdn...">` deja la
página con un `<video>` negro en cuanto el contenedor no tiene internet, que es exactamente
como se va a levantar. hls.js 1.6.16 va commiteado con su licencia Apache-2.0.

### El servidor no lleva `WriteTimeout`

Cortaría toda conexión SSE a los pocos segundos, que es justo lo que este servicio necesita
mantener abierta. Contra clientes lentos van `ReadHeaderTimeout` y el backpressure del hub,
que atacan el problema real sin romper las respuestas largas.

### Toda la ruta del stream está protegida, no sólo `/player`

Proteger únicamente la página dejaría el `.m3u8` y los `.ts` descargables sin cuenta, que es
precisamente lo que el requisito 4 busca impedir. El middleware cubre también
`/live/stream.m3u8`, `/live/segments/*` y `/live/events`, y responde **401 y no 302**: con un
redirect, hls.js intentaría parsear la página de login como playlist.

### Dos dependencias en total

`modernc.org/sqlite` y `golang.org/x/crypto`. Todo lo demás —routing, sesiones, cookies,
plantillas, SSE— es biblioteca estándar. SQLite entra por el driver en Go puro, que permite
`CGO_ENABLED=0`, produce un binario estático y cabe en un contenedor único sin
`docker-compose`.

---

## Ambigüedades del enunciado y cómo se interpretaron

1. **La sección B dice "el Livestreaming generado en NodeJS"**, pero el punto 1 y la sección
   A piden Go inequívocamente. Se tomó como residuo de una versión anterior del documento —el
   archivo se llama "Prueba v3"— y está todo en Go.
2. **El ejemplo de `.m3u8` no coincide con el texto:** el texto pide segmentos de 10 s y 3 por
   request; el ejemplo muestra `TARGETDURATION:6` y 4 segmentos. Manda el texto. Coincide
   además con el `segment.m3u8` provisto, que trae `TARGETDURATION:10`.
3. **"eliminar el último segmento (primero de la lista)"** se contradice; el paréntesis aclara
   la intención: es una ventana deslizante FIFO.
4. **Cuántos `.ts` debe haber en disco** no se dice, pero se exige implícitamente que sean más
   de 3 al pedir "agregar un segmento **nuevo**". Se usan los 64 provistos, en buffer circular,
   con `EXT-X-DISCONTINUITY` en la vuelta del ciclo.

---

## Limitaciones conocidas

Declararlas da más confianza que omitirlas.

- **La ventana de 3 segmentos no deja margen.** Tres de 10 s son exactamente el mínimo que
  recomienda la spec de HLS. Si al espectador se le cae la red unos segundos, se sale de la
  ventana. Es una restricción del enunciado y se respetó tal cual.
- **El contenido se repite cada 10,5 minutos.** Inherente a reproducir material finito en
  loop. La transición está señalizada y no produce cortes.
- **El contador de espectadores es por proceso.** Con varias réplicas cada una contaría los
  suyos.
- **Sin HTTPS**: se asume un proxy inverso por delante. `SECURE_COOKIES` ya está previsto.
- **El login cierra las sesiones de los otros dispositivos**, como efecto colateral de rotar
  la sesión contra *session fixation*. Discutible en un producto de streaming; se aceptó a
  propósito por ser la propiedad de seguridad demostrable.

---

## Desarrollo

```bash
go test ./... -count=1     # 155 tests
go vet ./...
gofmt -l .

# Para correrlo sin Docker (los defaults son rutas de contenedor):
DB_PATH=./tmp-zapping.db SEGMENTS_DIR="hls test" PORT=8080 go run ./cmd/server
```

El detector de carreras necesita un compilador de C, así que en Windows se corre en
contenedor:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26 go test -race -count=1 ./...
```

### Sobre los tests

155 en total. Lo que más valió la pena del proceso: **casi todos los defectos encontrados
fueron tests que no probaban lo que decían probar**, no errores de lógica. El método que los
encontró fue mutar el código a propósito y comprobar si algún test se quejaba. Tres ejemplos
reales de este proyecto:

- Escribir `data: {...}\n` en vez de `\n\n` dejaba la suite entera en verde y el panel muerto
  en el navegador: `EventSource` sólo despacha el mensaje al ver la línea en blanco.
- El test que hacía cumplir la regla de no poner `backdrop-filter` sobre el `<video>` no
  detectaba nada: su expresión regular se comía el comentario que precede a la regla.
- Agregar `WriteTimeout` al servidor no lo notaba ningún test, y habría cortado todas las
  conexiones SSE.

Está contado en detalle en [docs/06-decisiones.md](docs/06-decisiones.md).

---

## Estructura

```text
cmd/server/          cableado, apagado ordenado
internal/config/     lectura del entorno
internal/storage/    SQLite: apertura, pragmas, migraciones
internal/cuenta/     modelo de usuario, validación, alta
internal/auth/       bcrypt, sesiones, middleware
internal/hls/        motor del livestream (no conoce HTTP)
internal/viewers/    hub SSE (no conoce hls ni HTTP)
internal/web/        router, handlers, plantillas y assets embebidos
docs/                diseño, decisiones y verificación
scripts/             preparación de segmentos y verificación de humo
```
