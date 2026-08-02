# 02 — Motor HLS

**Primer bloque a implementar.** Es el núcleo de la prueba y la única pieza con riesgo
técnico real. Paquete `internal/hls`. No conoce HTTP.

## El problema

Tenemos 64 segmentos pregrabados (un VOD) y hay que servirlos como si fueran un
livestreaming infinito: una ventana deslizante de 3 segmentos que avanza en tiempo real,
con `EXT-X-MEDIA-SEQUENCE` siempre creciente.

```text
Pool en disco: seg0 … seg63          Ventana: 3 entradas (fija por enunciado)

t=0s      [seg0, seg1, seg2]         MEDIA-SEQUENCE:0
t=10s     [seg1, seg2, seg3]         MEDIA-SEQUENCE:1     ← sale seg0, entra seg3
t=20s     [seg2, seg3, seg4]         MEDIA-SEQUENCE:2
   ⋮
t=630s    [seg61, seg62, seg63]      MEDIA-SEQUENCE:61    ← se agota el pool
t=634.57s [seg62, seg63, seg0]       MEDIA-SEQUENCE:62    ← vuelta del ciclo
```

Nótese el salto de `630s` a `634.57s`: **`seg63` dura 4,566667s, no 10s.** Ese detalle es
el que define el diseño del reloj.

## Las tres decisiones de diseño

### 1. El reloj lo marca el tiempo de medios, no un ticker fijo

La regla no es "rotar cada 10 segundos" sino **"rotar cuando el segmento que sale terminó
de reproducirse"**. Con un `time.Ticker` de 10s fijos, el segmento corto provocaría que el
player consuma 4,57s de contenido mientras la ventana avanza 10s: se adelantaría 5,43s por
ciclo, se saldría de la ventana y se cortaría la reproducción.

Se resuelve con una **tabla de sumas acumuladas** construida una sola vez al arrancar.

### 2. La secuencia se deriva del reloj monotónico, no se incrementa

Con `seq++` los errores se acumulan. Derivando `seq` de `time.Since(inicio)`, el valor es
correcto siempre, aunque el proceso se congele o el timer se atrase. **La deriva deja de
ser pequeña y pasa a ser estructuralmente imposible.**

### 3. El estado no se muta: se reemplaza

La goroutine construye un `Snapshot` inmutable y lo publica con `atomic.Pointer.Store`.
Los lectores hacen `Load`. Nadie muta nada, no hace falta `RWMutex`, y las lecturas quedan
*wait-free* sin contención por muchos espectadores que haya.

Además el texto del `.m3u8` se pre-renderiza **dentro** del snapshot: como sólo cambia una
vez por rotación, cada request pasa a ser un `Write` de bytes ya listos.

## `pool.go` — el pool y la tabla de duraciones

Las duraciones **no se adivinan ni se sondean**: se leen del `segment.m3u8` provisto con el
material, que ya trae un `#EXTINF` exacto por segmento. Se ignora `#EXT-X-ENDLIST`, que es
lo que lo marca como VOD.

```go
type Segment struct {
    Name     string        // "segment0.ts"
    Duration time.Duration // 10s, o 4.566667s el último
}

type Pool struct {
    segments []Segment
    cum      []time.Duration // len = len(segments)+1; cum[0]=0, cum[n]=total
    total    time.Duration
    target   int             // EXT-X-TARGETDURATION: max duración, redondeada
}

// ParseManifest lee el m3u8 provisto y construye el pool.
// Falla si no hay segmentos o si algún EXTINF es inválido.
func ParseManifest(path string) (*Pool, error)
```

`cum[i]` es la suma de las duraciones de los segmentos `[0, i)`:

```text
d      = [10, 10, 10, …, 10, 4.566667]              64 duraciones
cum    = [0, 10, 20, 30, …, 620, 630, 634.566667]   65 valores
total  = 634.566667s
target = 10
```

### Localizar la posición en el ciclo

```go
// Locate devuelve la secuencia absoluta vigente en `elapsed` y cuánto falta
// para la próxima rotación.
func (p *Pool) Locate(elapsed time.Duration) (seq int64, until time.Duration) {
    n      := int64(len(p.segments))
    cycles := int64(elapsed / p.total)
    rem    := elapsed % p.total

    // menor i tal que cum[i+1] > rem; garantiza cum[i] <= rem < cum[i+1]
    i := sort.Search(len(p.segments), func(k int) bool {
        return p.cum[k+1] > rem
    })

    seq   = cycles*n + int64(i)
    until = p.cum[i+1] - rem
    return
}
```

Búsqueda binaria O(log n) sin asignaciones. Esta es la estructura de datos que responde al
criterio "estructura de datos" del enunciado: un arreglo de prefijos, no un contador ingenuo.

## `snapshot.go` — el snapshot inmutable

```go
type Snapshot struct {
    Seq      int64     // EXT-X-MEDIA-SEQUENCE
    DiscSeq  int64     // EXT-X-DISCONTINUITY-SEQUENCE
    HasDisc  bool      // hay una discontinuidad dentro de esta ventana
    Window   []Segment // exactamente WINDOW_SIZE elementos
    Playlist []byte    // el .m3u8 ya renderizado
    NextAt   time.Time // instante de la próxima rotación (para el panel SSE)
}
```

Una vez construido **no se modifica nunca**. `Window` y `Playlist` son de sólo lectura por
contrato: quien recibe el snapshot no los altera.

### Construcción de la ventana y la discontinuidad

La ventana son los segmentos en las posiciones `seq, seq+1, seq+2`, cada una mapeada al pool
con `índice = posición mod n`.

**Regla de discontinuidad:** al pasar de `seg63` a `seg0` los timestamps internos del video
saltan hacia atrás y el decodificador debe reinicializarse. Se emite
`#EXT-X-DISCONTINUITY` **antes** del segmento donde ocurre el salto, pero sólo si el salto
cae **dentro** de la ventana (posiciones 1 o 2). Si cayera en la posición 0, la etiqueta ya
salió del playlist y quedó contabilizada en `EXT-X-DISCONTINUITY-SEQUENCE`.

```go
DiscSeq = seq / int64(len(p.segments))   // ciclos completados antes del primer segmento
```

Verificación: `seq=0 → 0` (aún no hubo discontinuidad); `seq=n-1 → 0`; `seq=n → 1`
(ese segmento va después de la primera discontinuidad).

### Render del playlist

```text
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:62
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXTINF:10.000000,
segments/segment62.ts
#EXTINF:4.566667,
segments/segment63.ts
#EXT-X-DISCONTINUITY
#EXTINF:10.000000,
segments/segment0.ts
```

Sin `#EXT-X-ENDLIST`: su ausencia es justamente lo que le dice al player que el stream
está vivo. `TARGETDURATION` es la duración máxima del pool redondeada al entero más
cercano, según RFC 8216 — se mantiene en 10 aunque haya un segmento de 4,57s.

## `engine.go` — la goroutine del reloj

```go
type Engine struct {
    pool  *Pool
    now   func() time.Time      // inyectable: hace los tests deterministas
    start time.Time
    cur   atomic.Pointer[Snapshot]
    subs  chan<- Snapshot       // notifica al hub SSE cada rotación
}

func New(pool *Pool, opts ...Option) *Engine
func (e *Engine) Run(ctx context.Context)   // bloquea hasta ctx.Done()
func (e *Engine) Current() *Snapshot        // atomic.Load, wait-free
```

El bucle:

```go
for {
    elapsed    := e.now().Sub(e.start)
    seq, until := e.pool.Locate(elapsed)

    e.cur.Store(e.buildSnapshot(seq, e.now().Add(until)))
    e.notify()

    select {
    case <-time.After(until):   // duerme exactamente hasta la próxima rotación
    case <-ctx.Done():
        return
    }
}
```

`until` sale siempre de la tabla acumulada contra el reloj absoluto, así que cada despertar
se re-ancla al `start`: los atrasos no se acumulan.

## Servir los segmentos

`Pool` expone la resolución de nombre a archivo, con validación estricta contra *path
traversal*: sólo se aceptan nombres que estén en el pool. El handler HTTP (documento 04) usa
`http.ServeContent` sobre el `*os.File`, que copia por bloques y da *range requests* gratis.

## Tests

Con el reloj inyectado, todos corren en milisegundos.

| Test | Qué verifica |
| --- | --- |
| `TestParseManifest` | 64 segmentos, la última duración es 4,566667s, se ignora `ENDLIST` |
| `TestParseManifestInvalido` | Falla limpio si falta el archivo o un `EXTINF` es basura |
| `TestLocateInicio` | `t=0` → `seq=0`, `until=10s` |
| `TestLocateBordeExacto` | `t=10s` → `seq=1` (no `0`), sin ambigüedad en el borde |
| `TestLocateSegmentoCorto` | En `seg63`, `until=4.566667s` y **no** `10s` ← el caso que motivó el diseño |
| `TestLocateVueltaDeCiclo` | `t=T` → `seq=64`; `t=T+10s` → `seq=65`; nunca reinicia |
| `TestSecuenciaMonotona` | Recorriendo 3 ciclos, `seq` es estrictamente creciente |
| `TestDiscontinuidadPosicion` | La etiqueta aparece sólo cuando el salto cae en posición 1 o 2 |
| `TestDiscontinuitySequence` | `DiscSeq` correcto en `seq = 0`, `n-1`, `n`, `2n` |
| `TestPlaylistFormato` | Salida byte a byte contra un fixture esperado |
| `TestVentanaSiempreTres` | En cualquier `t`, la ventana tiene exactamente 3 entradas |
| `TestResolveTraversal` | `../../etc/passwd` y similares son rechazados |

## Criterios de aceptación

- [ ] El `.m3u8` sirve siempre exactamente 3 segmentos (30s nominales).
- [ ] `EXT-X-MEDIA-SEQUENCE` crece de a uno y **nunca** se reinicia, ni al dar la vuelta.
- [ ] La ventana avanza a los 4,57s cuando sale el segmento corto, no a los 10s.
- [ ] `EXT-X-DISCONTINUITY` aparece en la vuelta del ciclo, en la posición correcta.
- [ ] El playlist no incluye `EXT-X-ENDLIST`.
- [ ] `Current()` no toma locks.
- [ ] El paquete no importa `net/http`.
- [ ] `go test ./internal/hls/...` pasa en menos de un segundo.
