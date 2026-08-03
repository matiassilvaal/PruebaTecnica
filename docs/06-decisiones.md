# 06 — Decisiones y justificaciones

**Documento vivo.** Se completa durante todo el desarrollo, no al final. De aquí salen el
README del repositorio y el correo de entrega, que según las instrucciones recibidas debe
justificar el tiempo invertido.

---

## Ambigüedades del enunciado y cómo se interpretaron

Conviene dejarlas por escrito en el README: demuestra que se leyó con atención en lugar de
que se pasaron por alto.

### 1. "el Livestreaming generado en NodeJS"

La sección B menciona NodeJS, pero el punto 1 (*"hacer un proyecto en Go lang"*) y la
sección A (*"una aplicación Go lang"*) son inequívocos. Es casi con seguridad un residuo de
una versión anterior del documento — el archivo se llama "Prueba v3".

**Interpretación tomada:** todo en Go. Además, el lenguaje del backend es invisible para el
player: hls.js consume un `.m3u8` por HTTP y no sabe ni le importa qué lo generó.

### 2. El ejemplo de m3u8 no coincide con el texto

El texto pide segmentos de **10 segundos** y **3 por request**; el ejemplo muestra
`TARGETDURATION:6`, `EXTINF:6.000000` y **4 segmentos**.

**Interpretación tomada:** manda el texto, el ejemplo es ilustrativo. Se usan
`TARGETDURATION:10`, las duraciones reales de cada segmento y 3 entradas por playlist.
Coincide además con el `segment.m3u8` provisto junto al material, que trae `TARGETDURATION:10`.

### 3. "eliminar el último segmento (primero de la lista)"

La frase se contradice, pero el paréntesis aclara la intención: es una ventana deslizante
FIFO — sale el primero del playlist, entra uno nuevo al final, y `MEDIA-SEQUENCE` sube.

### 4. Cuántos segmentos debe haber en disco

El enunciado fija la **ventana** (3 entradas por request) pero no dice cuántos archivos
`.ts` deben existir. De hecho exige implícitamente que sean más de 3: pide *"agregar un
segmento **nuevo** al final"*, y con sólo 3 archivos no habría nada nuevo que agregar.

**Interpretación tomada:** se usan los 64 segmentos provistos, con un buffer circular que
recicla el pool para que el stream no termine nunca.

---

## Decisiones técnicas

### El reloj lo marca el tiempo de medios, no un ticker fijo

**Problema:** `segment63.ts` dura 4,566667s, no 10s. Con un `time.Ticker` de 10 segundos
fijos, el player consumiría ese segmento en 4,57s mientras la ventana avanzaría recién a los
10s. Se adelantaría 5,43s por ciclo, se saldría de la ventana y **la reproducción se
cortaría**.

**Decisión:** una tabla de sumas acumuladas de duraciones; la ventana rota exactamente
cuando el segmento que sale terminó de reproducirse.

**Beneficio adicional:** el motor soporta segmentos de cualquier duración sin cambios, así
que no hubo que descartar ningún archivo del material provisto.

### La secuencia se deriva del reloj monotónico, no se incrementa

Con `seq++` los errores de temporización se acumulan. Derivando `seq` de
`time.Since(inicio)` contra la tabla acumulada, el valor es correcto siempre, aunque el
proceso se congele o el timer se atrase. La deriva no queda pequeña: queda
**estructuralmente imposible**.

### Snapshot inmutable con publicación atómica

**Alternativa evaluada:** estado mutable protegido con `sync.RWMutex`.

**Decisión:** la goroutine construye un `Snapshot` inmutable y lo publica con
`atomic.Pointer.Store`; los lectores hacen `Load`. Nada se muta nunca.

**Por qué:** elimina el estado mutable compartido, elimina la contención de lock, y deja las
lecturas *wait-free* — un espectador o diez mil cuestan lo mismo. El costo es una asignación
pequeña por rotación, del orden de decenas de bytes.

### El `.m3u8` se pre-renderiza dentro del snapshot

Como el playlist sólo cambia una vez por rotación, generar su texto por request sería
repetir el mismo trabajo. Se renderiza una vez y cada petición queda en un `Write` de bytes
ya listos: cero formateo, cero asignaciones por request.

### `EXT-X-DISCONTINUITY` en la vuelta del ciclo

Al pasar de `segment63` a `segment0` los timestamps internos del video saltan hacia atrás y
el decodificador debe reinicializarse. Sin la etiqueta habría un glitch o un congelamiento.
Es el mismo mecanismo estándar con el que la industria inserta publicidad en vivo, y hls.js
lo atraviesa sin detenerse.

### El manifiesto y los segmentos son confianza de operador, no input de usuario

**Contexto:** `Pool.Resolve` traduce un nombre de segmento a una ruta en disco mediante una
lista blanca — sólo acepta nombres que ya están en el índice construido al parsear el
manifiesto — y no sanea la entrada más allá de eso. Un manifiesto adversarial que declarara
un segmento llamado `../secreto.txt` sí escaparía del directorio de segmentos si ese nombre
llegara a `Resolve`.

**Por qué no es un hueco:** `segment.m3u8` y los `.ts` se hornean **dentro de la imagen
Docker en build time** (ver `docs/05-docker-y-entrega.md`). No existe ninguna ruta de subida
en el diseño — ni un endpoint, ni un volumen editable por el usuario final — así que el
manifiesto es configuración fijada por quien arma la imagen (el operador), no un input que
llegue desde la red o desde una cuenta de usuario. El único actor que podría escribir un
`../secreto.txt` en el manifiesto es quien ya tiene control sobre el build de la imagen, y en
ese punto ya tiene control total del contenedor de cualquier forma.

**Decisión:** `Resolve` no necesita sanear rutas (normalizar `..`, rechazar separadores,
etc.) más allá de la lista blanca contra el índice del pool. Añadir ese saneamiento sería
defensa contra un actor que el modelo de amenaza de este proyecto no contempla.

### Los `.ts` se sirven con `http.ServeContent`

Copia por bloques desde un `*os.File` en vez de cargar el archivo en memoria, y habilita
*range requests* sin código adicional. Un segmento de 13 MB consume del orden de KB de RAM
del servidor.

### El hub SSE tiene una goroutine dueña, no un mutex

**Alternativa evaluada:** un `map[*cliente]struct{}` protegido con `sync.RWMutex`.

**Decisión:** una sola goroutine (`Hub.Run`) es dueña del conjunto de clientes; todo lo demás
—alta, baja, difusión— entra por canales.

**Por qué:** el mapa deja de necesitar sincronización porque nadie más lo toca, y las tres
operaciones se serializan solas sin que haya que razonar sobre el orden de los locks. Es el
patrón que Go recomienda para este caso y hace que el paquete se verifique con `-race` sin
ambigüedad. El costo es que las operaciones pasan por canales, irrelevante a este ritmo: un
evento cada ~10 s.

Un corolario que salió de la implementación: `Suscribir` es **síncrona respecto del alta**.
Cuando vuelve, el cliente ya está en el conjunto, el contador ya lo incluye y el estado
vigente ya está en su canal. Cuesta un viaje de ida y vuelta por espectador —no por
rotación— y elimina toda una clase de carreras entre quien se suscribe y la goroutine dueña:
sin esa espera, un suscriptor podía leer un contador viejo o recibir su evento de alta
detrás del de alguien que se suscribió después.

### Dos puntos de backpressure, no uno

El documento de diseño tenía uno. En la implementación resultaron ser dos, y por razones
distintas:

1. **El envío a cada cliente.** Un espectador que dejó de leer —red lenta, pestaña
   congelada— bloquearía el broadcast para todos los demás y acumularía eventos sin techo.
   Es la fuga de memoria clásica de este patrón.
2. **`Hub.Publicar`.** Lo llama el hook de rotación del motor, que corre síncronamente en la
   goroutine que hace avanzar el stream. Si se bloqueara, el stream se detendría **para todos
   los espectadores**, no sólo el panel.

Los dos descartan con `select`/`default`. Descartar es correcto porque cada evento trae el
**estado completo, no un delta**: el siguiente pone al día igual. Es la decisión que responde
al criterio de RAM del enunciado en la parte asíncrona del sistema, igual que
`http.ServeContent` lo hace en la parte de disco.

### El binario es autosuficiente: templates, CSS, JS y hls.js embebidos

`go:embed` mete los assets dentro del binario. El contenedor no depende de cuál sea el
directorio de trabajo, el Dockerfile **no necesita `COPY web/`**, y un `go run ./cmd/server`
desde cualquier carpeta funciona igual. Como `embed` no puede subir de directorio, los assets
viven bajo `internal/web/` en vez de en un `web/` de la raíz: la restricción de la
herramienta y la decisión de empaquetado apuntan al mismo lugar.

### El servidor HTTP no lleva `WriteTimeout`

Cortaría toda conexión SSE a los pocos segundos, que es justo lo que este servicio necesita
mantener abierta. Contra clientes lentos van `ReadHeaderTimeout` (slowloris) y el
backpressure del hub, que atacan el problema real sin romper las respuestas largas.

La ausencia está **fijada por un test** (`TestElServidorNoLlevaWriteTimeout`), y no por
prolijidad: agregarlo "por prudencia" es exactamente el cambio que alguien hace un día sin
que nada se queje, y el síntoma —conexiones SSE que se cortan solas— aparecería lejos de la
causa.

`IdleTimeout` sí va, y el mismo test lo exige: sólo corre **entre** peticiones de una conexión
keep-alive, así que no puede tocar un SSE en curso, y sin él una pestaña que se va sin cerrar
la conexión la deja retenida hasta que el sistema operativo la tire. Las dos aserciones viven
juntas a propósito, para que quien agregue un timeout no agregue el otro de paso.

### Errores de formulario con 422, no con 200

Un 200 diría que la petición se procesó correctamente, y no es cierto. 422 (Unprocessable
Content) es exacto y los navegadores renderizan el cuerpo igual, así que la corrección no
cuesta nada. Las credenciales incorrectas van con 401; sólo un fallo de la base da 500.

### Los estáticos revalidan con ETag en vez de confiar en un max-age

**El defecto que lo motivó:** un arreglo del player quedó invisible durante una hora para
quien ya había abierto la página. Los estáticos se servían con `max-age=3600` **y sin ETag ni
Last-Modified**, así que el navegador no tenía con qué preguntar si el archivo había
cambiado: usaba su copia vieja hasta que expirara.

Lo de Last-Modified no es un olvido, es una propiedad de `embed.FS`: sus archivos reportan
`ModTime` cero, y `http.ServeContent` omite la cabecera cuando la fecha es cero. Es decir que
embeber los assets —que es lo que hace autosuficiente al binario— elimina de paso el único
mecanismo de revalidación que Go daba gratis.

**Decisión:** `Cache-Control: no-cache` más un `ETag` con el SHA-256 del contenido, calculado
una vez al arrancar recorriendo el `embed.FS`.

`no-cache` no significa "no cachear": significa "guárdalo, pero pregunta antes de usarlo".
Con el ETag, esa pregunta se responde con un 304 de unos cientos de bytes, así que hls.js
—543 KB— se baja igual una sola vez.

**Alternativa evaluada:** `max-age` largo con la huella del contenido en el nombre del
archivo, que es lo que hace un bundler. Obligaría a reescribir las URL de las plantillas al
vuelo; para tres archivos, revalidar sale más barato y no puede quedar desincronizado.

### El `.m3u8` y los `.ts` se cachean al revés a propósito

El playlist con `no-cache, no-store`: uno cacheado le entrega al player una ventana vieja,
que lo lleva a pedir segmentos ya expirados y a cortar la reproducción. Los segmentos con
`private, max-age=31536000, immutable`: su contenido no cambia nunca, así que revisitar la
ventana no vuelve a costarle disco al servidor. Direcciones opuestas, cada una por una razón
concreta.

El `private` de los `.ts` no es decoración: esa ruta está detrás de la sesión, y una cookie
—a diferencia de un `Authorization`— no impide por sí sola que una caché compartida guarde la
respuesta. Un `public` ahí sería una autorización explícita para que un proxy sirviera un
segmento autenticado durante un año a cualquiera, que es justo lo que el requisito 4 prohíbe.
La caché del navegador, que es de donde sale el ahorro, funciona igual con `private`.

Los estáticos (`/static/*`) van con `no-cache` y un ETag del contenido, por la razón que
explica la sección anterior: sus nombres no llevan huella, así que un `max-age` los congelaba
sin forma de revalidar. `no-cache` no los descarga de nuevo cada vez — los revalida, y el 304
cuesta unos cientos de bytes.

### Toda la ruta del stream está protegida, no sólo `/player`

Proteger únicamente la página dejaría el `.m3u8` y los `.ts` accesibles sin cuenta, que es
exactamente lo que el requisito 4 busca impedir. El middleware cubre también
`/live/stream.m3u8`, `/live/segments/*` y `/live/events`.

### Sesiones en base de datos, no JWT

Para un sitio de tres páginas renderizadas en el servidor, las sesiones son la elección
correcta: se pueden **revocar** al cerrar sesión, cosa que un JWT no permite sin agregar una
lista de revocación que reintroduce el estado que el JWT venía a evitar.

En la base se guarda el SHA-256 del token, no el token: una filtración entrega valores
inservibles.

### Dos dependencias en total

`modernc.org/sqlite` y `golang.org/x/crypto`. Todo lo demás — routing, sesiones, cookies,
templates, SSE — es stdlib. `golang.org/x/crypto` está mantenido por el propio equipo de Go.

Escribir el manejo de sesiones en vez de importarlo son ~80 líneas y demuestra la mecánica
en lugar de esconderla tras una caja negra.

### SQLite en vez de Postgres

El enunciado pide *"un docker con el aplicativo funcionando"*, en singular. SQLite con
`modernc.org/sqlite` es Go puro, permite `CGO_ENABLED=0`, produce un binario estático y cabe
en un contenedor único sin `docker-compose` ni servicios externos.

### Sin Bootstrap

Con glassmorfismo habría que sobrescribir los componentes de Bootstrap para quitarles su
aspecto por defecto: más trabajo que escribir ~150 líneas de CSS propio para tres páginas
simples, y menos legible. hls.js queda como única dependencia de frontend.

### `backdrop-filter` nunca sobre el `<video>`

El desenfoque se recalcula cada vez que cambia lo que hay detrás. Sobre un video en
reproducción son 25-30 recálculos en GPU por segundo, y en una máquina modesta puede tirar
frames del propio video. Los paneles de vidrio van al lado; cualquier overlay sobre el video
usa un degradado semitransparente sin `backdrop-filter`.

### hls.js vendorizado, no por CDN

Si el evaluador levanta el contenedor sin internet, un CDN rompe la entrega. El enunciado
pide un docker **funcionando**.

### El reloj se inyecta como dependencia

Sin esto, probar la vuelta del ciclo exigiría esperar 10,5 minutos reales. Con esto la suite
de tests corre en milisegundos y es determinista.

---

### "Volver al vivo" se mide contra hls.js, no contra el borde del rango

**El defecto que lo motivó:** el botón aparecía siempre, desde el primer segundo y para todo
el mundo. Dos decisiones correctas por separado se contradecían.

`liveSyncDurationCount: 3` posiciona al player **a propósito** al comienzo de la ventana de
tres segmentos, que es donde hay más colchón antes de quedarse sin datos. Con una ventana del
mínimo que permite la spec, ese margen es todo lo que hay. Pero eso deja la reproducción
20-30 s por detrás de `seekable.end()`, y el botón comparaba justamente contra ese borde con
un umbral de 12 s. Resultado: en el instante en que la reproducción arrancaba **exactamente
donde debía**, la resta daba 20-30 y el botón se encendía. Y no se apagaba nunca.

**Decisión:** la referencia es `hls.liveSyncPosition` —dónde hls.js considera que está el
vivo—, no el final del rango buscable. Es la única contra la que "atrasado" significa algo.
El salto del botón va también a esa posición y no al borde absoluto, que puede no estar
descargado y del que hls.js devolvería la reproducción hacia atrás igual.

Mientras hls.js no publique esa posición —los primeros instantes— el botón queda oculto, en
vez de caer al borde, que es la referencia equivocada.

Safari reproduce HLS de forma nativa y no expone un equivalente, pero tampoco se aleja del
borde: ahí sí se usa el final del rango.

**Nota sobre qué significa "en vivo" acá:** con una ventana de tres segmentos, estar 20-30 s
por detrás del borde absoluto **es** la posición en vivo. La spec de HLS recomienda arrancar
a tres duraciones objetivo del final justamente por eso, y con 30 s de ventana no hay forma
de estar más cerca sin quedarse sin buffer a la primera. La latencia es una consecuencia del
tamaño de ventana que fija el enunciado, no una elección del player.

## Limitaciones conocidas

Vale la pena declararlas: reconocerlas da más confianza que omitirlas.

### La ventana de 3 segmentos no deja margen

Tres segmentos de 10s son **exactamente el mínimo** que recomienda la spec de HLS
(3 × `TARGETDURATION`). Funciona, pero si al espectador se le cae la red unos segundos, se
sale de la ventana. Es una restricción del enunciado y se respetó tal cual; un stream de
producción usaría 5 o 6 segmentos de ventana.

### El contenido se repite cada 10,5 minutos

Inherente a reproducir material finito en loop. La transición está señalizada con
`EXT-X-DISCONTINUITY` y no produce cortes, pero el video vuelve al principio.

### El contador de espectadores es por proceso

Con varias réplicas cada una contaría los suyos. Escalar horizontalmente requeriría un
backend compartido (Redis o similar), fuera del alcance de esta prueba.

### Sin HTTPS

Se asume un proxy inverso por delante en producción. `SECURE_COOKIES` ya está previsto.

### El login cierra las sesiones de los otros dispositivos

`DestruirDeUsuario` antes de `Crear` previene session fixation, pero como efecto colateral
entrar desde el celular desconecta el navegador. Se aceptó a propósito: en una prueba técnica
pesa más la propiedad de seguridad demostrable que la comodidad multi-dispositivo. Un
producto real rotaría sólo el token de la sesión en curso y dejaría `DestruirDeUsuario` para
el cambio de contraseña.

### Sin token CSRF en los formularios

La cookie de sesión es `SameSite=Lax`, lo que impide que un sitio externo envíe un POST
autenticado, y las tres acciones con efecto (registro, login, logout) son POST. Para un sitio
de tres páginas es suficiente; un producto con más superficie agregaría el token.

### El re-render del playlist por request no está cubierto por ningún test

Que el `.m3u8` se renderice una sola vez por rotación es una decisión de RAM, pero no hay
test que la fije: un re-render determinista produce exactamente los mismos bytes y no toca el
array compartido, así que ni `TestPlaylistEsElDelSnapshot` ni
`TestPlaylistNoMutaElSnapshotCompartido` lo verían. Detectarlo exigiría contar asignaciones
(`testing.AllocsPerRun` sobre el handler). Está anotado en el comentario del test para que
nadie lea de ahí una garantía que no existe.

---

## Verificación del bloque 02 (motor HLS)

Comandos con los que se verificó el paquete `internal/hls`. Valen para el README:
son la evidencia de lo que se afirma, no una promesa.

```bash
go test ./internal/hls/ -v          # 35/35, en menos de un segundo
go test ./internal/hls/ -cover      # 97,2 % de cobertura
go vet ./... && gofmt -l .          # sin hallazgos
go list -deps ./internal/hls | grep -x net/http   # sin resultados: no conoce HTTP
```

**Detector de carreras.** `-race` requiere un compilador C, que en Windows no viene con
Go. Se verifica en un contenedor Linux, que además confirma que la vía Docker del
requisito 1 funciona:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26 go test -race ./internal/hls/
```

Resultado sobre el commit final: **sin advertencias de carrera**. La afirmación de que
las lecturas son wait-free y no hay estado mutable compartido está verificada
empíricamente, no sólo argumentada por diseño.

## Verificación del bloque 04 (web y frontend), y del proyecto entero

Ejecutado al cerrar el bloque, sobre `feat/web-frontend`. Los números son los reales.

```bash
$ go test ./... -count=1
ok  zapping-live/cmd/server 2.1s   ok  zapping-live/internal/auth 3.4s
ok  zapping-live/internal/config 1.0s   ok  zapping-live/internal/cuenta 2.5s
ok  zapping-live/internal/hls 2.0s   ok  zapping-live/internal/storage 1.0s
ok  zapping-live/internal/viewers 1.3s   ok  zapping-live/internal/web 2.9s
```

**155 tests** en total, 0 fallos, la suite completa en ~5 s de reloj. Repartidos:
`web` 53, `hls` 35, `auth` 25, `cuenta` 15, `viewers` 11, `storage` 9, `cmd/server` 4,
`config` 3.

```bash
CGO_ENABLED=0 go build ./...   # compila
go vet ./...                   # sin hallazgos
gofmt -l .                     # sin salida
```

Cobertura de los paquetes nuevos: `viewers` 95,8 %, `config` 93,9 %, `web` 77,0 %,
`cmd/server` 21,4 % — este último es cableado, y el `run()` que lo arma exigiría levantar el
proceso para probarlo; de ese archivo sí están cubiertos `limpiarSesiones` y la ausencia de
`WriteTimeout`.

**La regla de dependencias se sostiene**, y se comprueba en vez de afirmarse:

```bash
go list -deps ./internal/hls | grep -x net/http        # vacío: hls no conoce HTTP
go list -deps ./internal/viewers | grep zapping-live  # sólo él mismo, ningún otro del proyecto
```

`internal/viewers` no importa **ningún** otro paquete del proyecto: no sabe que existe `hls`
ni que existe HTTP. Es lo que permite probar el hub sin levantar un servidor ni arrancar el
motor, y lo que obliga a que el puente entre los dos (`web.HookDeRotacion`) viva en el único
paquete que ya conoce a ambos.

**Sin dependencias nuevas.** El bloque 04 no agregó ninguna: siguen siendo dos.

```bash
git diff feat/auth-db..HEAD -- go.mod go.sum   # sin salida
```

*(La comprobación contra `master` que pedía el plan no aplica tal cual: en `master` todavía
no existe `go.mod`, porque los bloques 02 y 03 viven en ramas propias sin mergear. El
contraste válido es contra la rama de la que sale ésta, y da vacío.)*

**Detector de carreras**, en contenedor Linux porque `-race` necesita un compilador C que
Windows no trae con Go:

```bash
$ docker run --rm -v "$PWD:/src" -w /src golang:1.26 go test -race -count=1 ./...
ok  zapping-live/cmd/server 3.7s        ok  zapping-live/internal/auth 18.2s
ok  zapping-live/internal/config 1.0s   ok  zapping-live/internal/cuenta 1.4s
ok  zapping-live/internal/hls 1.6s      ok  zapping-live/internal/storage 1.4s
ok  zapping-live/internal/viewers 1.1s  ok  zapping-live/internal/web 16.9s
```

Los 155 tests **sin una sola advertencia de carrera**. Es la evidencia empírica de que la
goroutine dueña del hub reemplaza de verdad al mutex y de que el snapshot del motor no
comparte estado mutable.

**Apagado ordenado**, verificado en contenedor porque Windows no entrega una señal de consola
a un proceso lanzado en segundo plano:

```bash
$ docker run -d --name z -e DB_PATH=/tmp/z.db -e "SEGMENTS_DIR=/src/hls test" \
    golang:1.26 sh -c 'CGO_ENABLED=0 go build -o /tmp/server ./cmd/server && exec /tmp/server'
$ docker stop -t 15 z        # devolvió en 586 ms, con 15 s de plazo
2026/08/03 08:18:47 pool cargado: 64 segmentos desde /src/hls test
2026/08/03 08:18:47 escuchando en http://localhost:8080
2026/08/03 08:18:48 señal recibida, apagando
2026/08/03 08:18:48 apagado limpio
```

Código de salida 0. El servicio cierra en menos de un segundo con 15 disponibles, así que
`docker stop` nunca llega a escalar a `SIGKILL` y el WAL de SQLite se cierra ordenadamente.

**Hallazgo de esa prueba, importante para el bloque 05:** la misma verificación con
`go run ./cmd/server` como PID 1 **falla** — ninguna línea de apagado y código de salida 2.
`docker stop` señaliza sólo al proceso 1 y `go run` no relaya la señal al binario hijo. Es
justo el tipo de error que se ve sano en desarrollo y pierde datos en producción.

## Memoria medida, no argumentada

El manejo de RAM es uno de los cuatro criterios evaluados, así que conviene un número real y
no sólo el razonamiento. Medido sobre el contenedor en marcha, con **dos espectadores
reproduciendo de verdad** desde un navegador:

| Métrica | Valor |
| --- | --- |
| Heap del proceso (`anon` del cgroup) | **5,2 MiB** |
| Total del contenedor (`docker stats`) | **5,9 MiB** |
| `VmRSS` del proceso | 15,3 MiB |
| Segmento promedio que está sirviendo | 7,5 MB |
| Segmento más grande | 12,9 MB |

**El servidor usa menos memoria que uno solo de los segmentos que entrega.** El más grande
pesa 12,9 MB y el contenedor entero se mueve en 5,9 MiB. Si los `.ts` se cargaran en memoria
para servirlos, cada petición produciría un pico de entre 7 y 13 MB por espectador; no
aparece ninguno. Es la comprobación empírica de que `http.ServeContent` copia por bloques
desde el `*os.File` en vez de leer el archivo entero.

Los 15,3 MiB de `VmRSS` frente a 5,2 de heap son el binario de 12,6 MB mapeado en memoria:
páginas de archivo, compartidas y descartables por el kernel, no presión de heap. Que el
binario sea grande es consecuencia de embeber hls.js y el resto de los assets, y se paga en
disco, no en RAM.

Cinco muestras a lo largo de ~40 segundos —varias rotaciones de ventana, con sus snapshots
nuevos y sus difusiones por SSE— dieron 4,90 · 5,03 · 5,16 · 5,16 · 5,16 MiB. Sube al
principio y se aplana: no hay crecimiento sostenido, que es lo que delataría una fuga en el
hub o en los handlers.

```bash
docker stats <contenedor> --no-stream
docker exec <contenedor> sh -c 'grep "^anon " /sys/fs/cgroup/memory.stat'
docker exec <contenedor> sh -c 'grep VmRSS /proc/1/status'
```

## Cómo se encontraron los defectos: mutar el código a propósito

Vale la pena dejarlo escrito, porque es lo que efectivamente encontró los problemas y no una
metodología declarada de adorno.

Casi todos los defectos que aparecieron durante la implementación de este bloque **no fueron
errores de lógica: fueron tests que no probaban lo que decían probar**. Se cerraron alrededor
de diecisiete mutaciones silenciosas —cambios deliberados al código de producción que no
hacían fallar ningún test—. Las cuatro más consecuentes:

1. **El terminador del SSE.** Escribir `data: {...}\n` en vez de `\n\n` dejaba la suite entera
   en verde, mientras el `EventSource` de un navegador real no habría despachado un solo
   mensaje: el panel habría estado muerto, sin test rojo y sin una línea de log. Hoy lo fija
   `TestSSEElMensajeTerminaEnLineaEnBlanco`.
2. **`TestNingunVidrioSeSuperponeAlVideo`**, el test que hace cumplir la regla de CSS más
   estricta del proyecto, no detectaba nada: su expresión regular capturaba el comentario
   anterior como parte del selector, así que el nombre prohibido nunca coincidía. Poner
   `backdrop-filter` en `.marco` pasaba limpio.
3. **`WriteTimeout`.** Agregarlo al `http.Server` no rompía ningún test, y habría cortado en
   silencio todas las conexiones SSE a los pocos segundos. Ahora hay un test que fija su
   ausencia.
4. **La garantía de que `Hub.Publicar` nunca bloquea** —la propiedad sobre la que se apoya
   todo el diseño asíncrono— no tenía test aislado. Se agregó
   `TestPublicarNoBloqueaConElHubDetenido`.

El método fue pedirle a cada revisión que **mutara el código deliberadamente y comprobara si
algún test se quejaba**: quitar un `default` de un `select`, borrar una llamada, cambiar una
cabecera, sustituir `ServeContent` por `io.Copy`. Si la mutación no rompe nada, el test que
debía cubrirla miente y hay que arreglarlo antes que al código.

Es más barato de aplicar de lo que parece y responde a la única pregunta que importa de una
suite de tests: no cuántos hay ni qué cobertura reportan, sino **qué defectos serían capaces
de detectar**.

## Restricciones que los bloques 02 y 03 dejaron, y dónde se cumplen

Ya están todas honradas por el bloque 04. Se dejan escritas porque son la mitad del valor de
haberlas anotado: sin este registro, cada una sería una convención que vive sólo en la cabeza
de quien la escribió.

Del bloque 02 (motor HLS):

- **`Engine.Run` debe llamarse exactamente una vez** por instancia. La garantía de un solo
  escritor sobre el estado del motor es una convención documentada, no forzada por código:
  dos goroutines corriendo `Run` romperían la monotonía de `EXT-X-MEDIA-SEQUENCE`.
- **El hook `onRotate` corre síncronamente en la goroutine de rotación.** El hub SSE debe
  reenviar el snapshot a su propio canal sin bloquear: si bloquea, detiene el avance del
  stream para todos; si entra en pánico, tumba la goroutine del motor.
- **El playlist debe servirse desde una ruta hermana de `segments/`** — por ejemplo
  `/live/stream.m3u8` con los segmentos en `/live/segments/`. Las URI del `.m3u8` son
  relativas; montarlo en otra ruta produce 404 silenciosos.
- **`Snapshot.Window` y `Snapshot.Playlist` son de sólo lectura.** Mutarlos corrompe el
  estado que ven todos los lectores concurrentes, porque comparten el array subyacente.

Las cuatro se cumplen en `cmd/server` y en `internal/web`: `Engine.Run` tiene un único
llamante en todo el proyecto; `Hub.Publicar` no bloquea y el hook no tiene nada que pueda
entrar en pánico; el router registra `/live/stream.m3u8` y `/live/segments/{name}` como rutas
hermanas; y `HookDeRotacion` copia los nombres de `Snapshot.Window` en vez de pasar el slice
compartido.

**Hay test para dos de las cuatro**, y conviene decir cuáles: el hook no bloqueante
(`TestHookDeRotacionNoBloqueaConElHubDetenido`) y la copia del snapshot
(`TestHookDeRotacionNoMutaElSnapshot`). Las otras dos siguen siendo **convenciones que ningún
test fija**: que `Engine.Run` tenga un único llamante es una propiedad estática que nadie
comprueba, y la relación hermana entre `/live/stream.m3u8` y `/live/segments/` está clavada
por separado en dos tests distintos, sin nada que ate una ruta a la otra. Mover ambas de forma
coordinada rompería los tests por 404, pero por la razón equivocada.

Del bloque 03 (auth y base de datos):

- **El alta de usuarios va por `cuenta.Registrar`, no por `Store.Crear`.** `Crear` recibe el
  hash ya calculado y no valida: existe como vía de bajo nivel para los tests. Usarlo desde
  un handler permitiría guardar la contraseña en claro y saltarse las reglas de validación.
- **El handler de login debe llamar a `auth.VerificarEnVacio()` cuando el email no existe.**
  Sin eso, un email inexistente responde en microsegundos y uno registrado paga los ~370 ms
  de bcrypt: el tiempo de respuesta revela qué cuentas existen aunque el mensaje de error sea
  idéntico. Este bloque le dio su **primer llamante**: `loginEnviar`, en la rama de
  `ErrNoEncontrado`.
- **El login debe rotar la sesión.** `Sessions.DestruirDeUsuario` antes de `Crear` previene
  session fixation. Nota de la revisión: como efecto colateral desconecta los demás
  dispositivos del usuario, cosa discutible en un producto de streaming; si molesta, basta
  con rotar el token y dejar `DestruirDeUsuario` para el cambio de contraseña.
- **`Sessions.Limpiar` no la ejecutaba nadie.** Ahora la corre `limpiarSesiones` en
  `cmd/server`: un barrido de entrada más un ticker de una hora, cancelable por contexto. Sin
  ella la tabla `sessions` crece sin límite, porque cada login deja una fila que nadie borra
  al vencer.
- **`Sessions.Resolver` devuelve `(int64, bool, error)`.** El tercer valor distingue "la base
  falló" de "no hay sesión": ignorarlo produciría un bucle de redirección al login sin una
  sola línea de log si SQLite se cae.
- **La cookie se emite con `Guard.PonerCookie(w, token)`**, que toma el TTL de `Sessions`.
  No pases el TTL por separado: si diverge, la cookie y la fila caducan en momentos distintos.

## Restricciones que hereda el bloque 05 (Docker y entrega)

- **El Dockerfile ya NO necesita `COPY web/`.** Los assets van embebidos en el binario con
  `go:embed` y viven bajo `internal/web/`. Copiar un directorio `web/` que no existe haría
  fallar el build.
- **`SEGMENTS_DIR` debe apuntar al directorio que contiene `segment.m3u8`.** El servidor arma
  la ruta como `filepath.Join(SEGMENTS_DIR, "segment.m3u8")` y **no levanta** si no lo
  encuentra. Es deliberado: un servidor arriba sirviendo 404 parece sano —responde, el
  healthcheck pasa— y el problema aparecería recién al intentar ver el stream.
- **El healthcheck consulta la base.** Si el volumen de `/data` no es escribible por el
  usuario `app`, `/healthz` responde 503 y Docker marca el contenedor como unhealthy. Es la
  señal correcta, pero conviene saber que ese es el síntoma de un problema de permisos y no
  de la base en sí.
- **`SECURE_COOKIES=true` sin HTTPS por delante rompe el login**: la cookie no viaja y nadie
  puede entrar. El default es `false` por eso.
- **El apagado depende de que la señal llegue al proceso 1, y `go run` no sirve.** Verificado
  empíricamente en contenedor (ver más abajo): con `go run ./cmd/server` como PID 1 el
  `docker stop` no produce ni una línea de apagado y el contenedor sale con código 2, porque
  `docker stop` señaliza sólo a PID 1 y `go run` no relaya la señal al binario hijo. Con el
  binario compilado como PID 1 el apagado es limpio. Traducción para el Dockerfile:
  `ENTRYPOINT ["/app/server"]` en **forma exec** y con el binario ya construido — nunca la
  forma shell (la intercepta `/bin/sh`) ni `go run`.
- **Las variables que el servidor lee** son `PORT` (8080), `DB_PATH` (`/data/zapping.db`),
  `SEGMENTS_DIR` (`/app/segments`), `SESSION_TTL` (24h), `SECURE_COOKIES` (false) y
  `WINDOW_SIZE` (3). Una variable **ausente** cae al default; una **presente pero ilegible**
  aborta el arranque, para que nadie quede convencido de haber configurado algo que nunca se
  aplicó.

## Optimización conocida y no aplicada

`Guard.proteger` hace dos consultas por request protegido: una a `sessions` y otra a `users`.
En `/live/segments/{name}` eso son dos consultas por segmento y por espectador. Un `JOIN`
único las reduce a una y de paso elimina una ventana TOCTOU entre ambas:

```sql
SELECT u.id, u.name, u.email, u.created_at, s.expires_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?
```

Se dejó sin aplicar por no reabrir la API de sesiones al cierre del bloque. La ventana TOCTOU
es benigna —un request pasa y el siguiente ya falla— pero la consulta doble sí es medible en
la ruta caliente de los segmentos.

## Notas para el correo de entrega

Puntos a cubrir, según lo que pidieron explícitamente:

1. **El tiempo y su justificación.** Registrar las horas reales y en qué se fueron. Lo que
   se pidió es que el tiempo esté *explicado*, no que sea corto ni continuo.
2. **Las ambigüedades detectadas** y la interpretación tomada en cada una — es la señal más
   barata de que el enunciado se leyó con atención.
3. **El caso del segmento de 4,57s**: probablemente el detalle técnico más interesante del
   desarrollo, porque descarta la solución obvia (un ticker de 10s) por una razón concreta
   y verificable.
4. **Cómo se verificó**, no sólo que se verificó: 155 tests sin carreras bajo `-race` en
   Linux, la regla de dependencias comprobada con `go list -deps`, y sobre todo el método de
   mutar el código para ver si algún test se queja. Es lo que separa una suite que mide algo
   de una que sólo sube un número de cobertura.
5. **Copia a Nacho y Claudio**, como se indicó.
