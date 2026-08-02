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

### Los `.ts` se sirven con `http.ServeContent`

Copia por bloques desde un `*os.File` en vez de cargar el archivo en memoria, y habilita
*range requests* sin código adicional. Un segmento de 13 MB consume del orden de KB de RAM
del servidor.

### Backpressure en el hub SSE

Cada cliente tiene un canal con buffer acotado; si se llena, el evento se descarta. Sin eso,
un solo cliente lento bloquearía el broadcast para todos y las goroutines se acumularían.
Es la fuga de memoria clásica de este patrón.

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

---

## Notas para el correo de entrega

Puntos a cubrir, según lo que pidieron explícitamente:

1. **El tiempo y su justificación.** Registrar las horas reales y en qué se fueron. Lo que
   se pidió es que el tiempo esté *explicado*, no que sea corto ni continuo.
2. **Las ambigüedades detectadas** y la interpretación tomada en cada una — es la señal más
   barata de que el enunciado se leyó con atención.
3. **El caso del segmento de 4,57s**: probablemente el detalle técnico más interesante del
   desarrollo, porque descarta la solución obvia (un ticker de 10s) por una razón concreta
   y verificable.
4. **Copia a Nacho y Claudio**, como se indicó.
