// Package hls convierte un conjunto de segmentos pregrabados en un
// livestreaming HLS con ventana deslizante. No conoce HTTP.
package hls

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const extinfPrefix = "#EXTINF:"

// Segment es una entrada del pool: un archivo .ts y su duración exacta.
type Segment struct {
	Name     string
	Duration time.Duration
}

// Pool es el conjunto inmutable de segmentos disponibles. Guarda una tabla de
// duraciones acumuladas que permite localizar la posición del stream en
// O(log n) sin recorrer la lista ni asumir que todos duran lo mismo.
type Pool struct {
	segments []Segment
	index    map[string]int
	cum      []time.Duration // len(segments)+1; cum[0]=0, cum[n]=total
	total    time.Duration
	target   int
	dir      string
}

// ParseManifest construye el pool desde un archivo m3u8. Las duraciones se
// leen de las etiquetas EXTINF en vez de asumirse: es la fuente de verdad que
// vino con el material. EXT-X-ENDLIST se ignora deliberadamente, porque el
// pool se va a servir como live y no como VOD.
func ParseManifest(path string) (*Pool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("abriendo manifiesto %s: %w", path, err)
	}
	defer f.Close()

	var (
		segments []Segment
		pending  time.Duration
		havePend bool
	)

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		txt := strings.TrimSpace(sc.Text())
		switch {
		case txt == "":
			continue
		case strings.HasPrefix(txt, extinfPrefix):
			d, err := parseExtinf(txt)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
			pending, havePend = d, true
		case strings.HasPrefix(txt, "#"):
			continue // resto de etiquetas, incluida EXT-X-ENDLIST
		default:
			if !havePend {
				return nil, fmt.Errorf("%s:%d: segmento %q sin #EXTINF previo", path, line, txt)
			}
			segments = append(segments, Segment{Name: txt, Duration: pending})
			havePend = false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("leyendo %s: %w", path, err)
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("%s: no contiene segmentos", path)
	}
	return newPool(segments, filepath.Dir(path)), nil
}

// parseExtinf extrae la duración de una línea "#EXTINF:10.000000,titulo".
func parseExtinf(line string) (time.Duration, error) {
	raw := strings.TrimPrefix(line, extinfPrefix)
	if i := strings.IndexByte(raw, ','); i >= 0 {
		raw = raw[:i]
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("EXTINF inválido %q: %w", line, err)
	}
	if secs <= 0 {
		return 0, fmt.Errorf("EXTINF no positivo %q", line)
	}
	// math.Round evita que la imprecisión de float64 deje 4.566667s en
	// 4566666999ns en vez de 4566667000ns.
	return time.Duration(math.Round(secs * float64(time.Second))), nil
}

func newPool(segments []Segment, dir string) *Pool {
	cum := make([]time.Duration, len(segments)+1)
	index := make(map[string]int, len(segments))
	var max time.Duration
	for i, s := range segments {
		cum[i+1] = cum[i] + s.Duration
		index[s.Name] = i
		if s.Duration > max {
			max = s.Duration
		}
	}
	return &Pool{
		segments: segments,
		index:    index,
		cum:      cum,
		total:    cum[len(segments)],
		// RFC 8216: TARGETDURATION es la duración máxima redondeada al entero
		// más cercano, no la del último segmento.
		target: int(math.Round(max.Seconds())),
		dir:    dir,
	}
}

// Len es la cantidad de segmentos del pool.
func (p *Pool) Len() int { return len(p.segments) }

// Total es la duración de una vuelta completa al pool.
func (p *Pool) Total() time.Duration { return p.total }

// Target es el valor de EXT-X-TARGETDURATION.
func (p *Pool) Target() int { return p.target }

// At devuelve el segmento en la posición i del pool.
func (p *Pool) At(i int) Segment { return p.segments[i] }

// Locate devuelve la secuencia absoluta vigente tras `elapsed` desde el inicio
// del stream, y cuánto falta para la próxima rotación.
//
// La posición se DERIVA del reloj contra la tabla acumulada en vez de
// incrementarse. Dos consecuencias:
//
//   - Los errores de temporización no se acumulan: el valor es correcto
//     aunque el proceso se congele o un timer se atrase.
//   - Los segmentos de duración distinta funcionan sin caso especial, porque
//     `until` sale de la duración real del segmento vigente y no de una
//     constante.
func (p *Pool) Locate(elapsed time.Duration) (seq int64, until time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	n := int64(len(p.segments))
	cycles := int64(elapsed / p.total)
	rem := elapsed % p.total

	// Menor i tal que cum[i+1] > rem, lo que garantiza cum[i] <= rem < cum[i+1].
	i := sort.Search(len(p.segments), func(k int) bool {
		return p.cum[k+1] > rem
	})

	return cycles*n + int64(i), p.cum[i+1] - rem
}

// Resolve traduce el nombre de un segmento a su ruta en disco.
//
// Sólo acepta nombres presentes en el pool. Es una lista blanca, no un
// saneamiento: cualquier intento de path traversal falla porque el nombre
// simplemente no está en el índice, sin depender de limpiar la entrada bien.
func (p *Pool) Resolve(name string) (string, bool) {
	i, ok := p.index[name]
	if !ok {
		return "", false
	}
	return filepath.Join(p.dir, p.segments[i].Name), true
}
