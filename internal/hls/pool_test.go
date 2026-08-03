package hls

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fixture = "testdata/manifest.m3u8"

func TestParseManifest(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if got := p.Len(); got != 5 {
		t.Errorf("Len() = %d, quiero 5", got)
	}
	if got := p.At(0); got.Name != "segment0.ts" || got.Duration != 10*time.Second {
		t.Errorf("At(0) = %+v, quiero {segment0.ts 10s}", got)
	}
	// El segmento corto es el caso que motiva todo el diseño del reloj.
	want := 4566667 * time.Microsecond
	if got := p.At(4); got.Name != "segment4.ts" || got.Duration != want {
		t.Errorf("At(4) = %+v, quiero {segment4.ts %v}", got, want)
	}
}

func TestParseManifestTotalYTarget(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	want := 44566667 * time.Microsecond
	if got := p.Total(); got != want {
		t.Errorf("Total() = %v, quiero %v", got, want)
	}
	// TARGETDURATION es la duración máxima redondeada, no la del último segmento.
	if got := p.Target(); got != 10 {
		t.Errorf("Target() = %d, quiero 10", got)
	}
}

func TestParseManifestIgnoraEndlist(t *testing.T) {
	// El fixture trae #EXT-X-ENDLIST. No debe aparecer como segmento
	// ni impedir el parseo: el pool se sirve como live.
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	for i := 0; i < p.Len(); i++ {
		if p.At(i).Name == "#EXT-X-ENDLIST" {
			t.Fatal("ENDLIST entró como segmento")
		}
	}
}

func TestParseManifestArchivoInexistente(t *testing.T) {
	if _, err := ParseManifest("testdata/no-existe.m3u8"); err == nil {
		t.Fatal("quiero error para un archivo inexistente")
	}
}

func TestParseManifestExtinfInvalido(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malo.m3u8")
	contenido := "#EXTM3U\n#EXTINF:no-es-un-numero,\nsegment0.ts\n"
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(path); err == nil {
		t.Fatal("quiero error para un EXTINF inválido")
	}
}

func TestParseManifestExtinfNoFinitoONoPositivo(t *testing.T) {
	// strconv.ParseFloat acepta "NaN"/"Inf"/"-Inf" sin error, y `secs <= 0` no
	// atrapa NaN porque toda comparación con NaN es falsa. Sin la guarda
	// explícita, math.Round(NaN*1e9) produce una Duration absurda (mínimo
	// int64), `cum` deja de ser estrictamente creciente y la precondición de
	// sort.Search en Locate se rompe: el servidor panickea al arrancar en vez
	// de fallar limpio como promete docs/01-diseno.md.
	casos := []struct {
		nombre string
		extinf string
	}{
		{"NaN", "NaN"},
		{"Inf", "Inf"},
		{"menos_Inf", "-Inf"},
		{"negativo", "-5"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "malo.m3u8")
			contenido := fmt.Sprintf("#EXTM3U\n#EXTINF:%s,\nsegment0.ts\n", c.extinf)
			if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ParseManifest(path); err == nil {
				t.Fatalf("quiero error para EXTINF %q, no positivo ni finito", c.extinf)
			}
		})
	}
}

func TestParseManifestSegmentoSinExtinf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huerfano.m3u8")
	if err := os.WriteFile(path, []byte("#EXTM3U\nsegment0.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(path); err == nil {
		t.Fatal("quiero error para un segmento sin EXTINF previo")
	}
}

func TestParseManifestSinSegmentos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vacio.m3u8")
	if err := os.WriteFile(path, []byte("#EXTM3U\n#EXT-X-VERSION:3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(path); err == nil {
		t.Fatal("quiero error para un manifiesto sin segmentos")
	}
}

func TestLocate(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	const corto = 4566667 * time.Microsecond // duración de segment4.ts
	total := p.Total()                       // 44.566667s

	casos := []struct {
		nombre    string
		elapsed   time.Duration
		wantSeq   int64
		wantUntil time.Duration
	}{
		{"inicio", 0, 0, 10 * time.Second},
		{"mitad del primer segmento", 4 * time.Second, 0, 6 * time.Second},
		// En el borde exacto ya se rotó: 10s pertenece al segmento 1.
		{"borde exacto", 10 * time.Second, 1, 10 * time.Second},
		{"tercer segmento", 25 * time.Second, 2, 5 * time.Second},
		// EL CASO CLAVE: sobre el segmento corto la ventana debe avanzar a los
		// 4,566667s y no a los 10s. Con un ticker fijo esto se rompería.
		{"segmento corto", 40 * time.Second, 4, corto},
		{"segmento corto a mitad", 42 * time.Second, 4, corto - 2*time.Second},
		// Vuelta del ciclo: la secuencia sigue creciendo, no se reinicia.
		{"vuelta del ciclo", total, 5, 10 * time.Second},
		{"segunda vuelta avanzada", total + 25*time.Second, 7, 5 * time.Second},
		{"tercera vuelta", 2*total + 40*time.Second, 14, corto},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			seq, until := p.Locate(c.elapsed)
			if seq != c.wantSeq {
				t.Errorf("seq = %d, quiero %d", seq, c.wantSeq)
			}
			if until != c.wantUntil {
				t.Errorf("until = %v, quiero %v", until, c.wantUntil)
			}
		})
	}
}

func TestLocateSecuenciaEstrictamenteCreciente(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Recorre tres ciclos completos en pasos de 1s y verifica que la
	// secuencia nunca retrocede ni se reinicia al dar la vuelta.
	var anterior int64 = -1
	fin := 3 * p.Total()
	for t0 := time.Duration(0); t0 < fin; t0 += time.Second {
		seq, until := p.Locate(t0)
		if seq < anterior {
			t.Fatalf("en %v la secuencia retrocedió: %d después de %d", t0, seq, anterior)
		}
		if until <= 0 {
			t.Fatalf("en %v until = %v, debe ser positivo", t0, until)
		}
		anterior = seq
	}
	// Tras 3 vueltas de 5 segmentos debe estar cerca de 15.
	if anterior < 14 {
		t.Errorf("tras 3 ciclos la secuencia llegó a %d, esperaba al menos 14", anterior)
	}
}

func TestResolve(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	path, ok := p.Resolve("segment2.ts")
	if !ok {
		t.Fatal("Resolve(segment2.ts) = false, quiero true")
	}
	if filepath.Base(path) != "segment2.ts" {
		t.Errorf("Resolve devolvió %q", path)
	}
	if dir := filepath.Base(filepath.Dir(path)); dir != "testdata" {
		t.Errorf("Resolve apunta a %q, esperaba dentro de testdata", path)
	}
}

func TestResolveRechazaDesconocidos(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Sólo se aceptan nombres que estén en el pool. Eso hace el path traversal
	// imposible por construcción, no por saneamiento de la entrada.
	maliciosos := []string{
		"../../etc/passwd",
		"..\\..\\windows\\system32\\config\\sam",
		"/etc/passwd",
		"segment2.ts/../../../etc/passwd",
		"testdata/segment2.ts",
		"segment99.ts",
		"",
		".",
		"..",
	}
	for _, m := range maliciosos {
		if path, ok := p.Resolve(m); ok {
			t.Errorf("Resolve(%q) = %q, true; quiero false", m, path)
		}
	}
}

// TestLocateRotacionesEncadenadas recorre el ciclo rotación a rotación,
// encadenando cada `until` como haría Run, durante más de 2 vueltas
// completas del fixture (5 segmentos). Antes se llamaba TestLocateNoDeriva y
// sólo comprobaba el `seq` final tras 200 saltos — un nombre que prometía más
// de lo que la prueba aislaba, porque pasaría igual con un ticker de
// duración promedio fija. Verifica los dos invariantes centrales del diseño:
//
//  1. seq sube EXACTAMENTE +1 en cada rotación, sin saltos ni reinicios al
//     dar la vuelta del ciclo.
//  2. until siempre coincide con la duración real del segmento que queda en
//     la cabeza de la ventana: es el invariante del que depende que el
//     player no sufra cortes, porque el motor duerme exactamente lo que se
//     tarda en consumir ese segmento.
func TestLocateRotacionesEncadenadas(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	n := int64(p.Len())

	var elapsed time.Duration
	var seqAnterior int64 = -1
	rotaciones := int(2*n) + 3 // más de 2 vueltas completas
	for i := 0; i < rotaciones; i++ {
		seq, until := p.Locate(elapsed)

		if seqAnterior >= 0 && seq != seqAnterior+1 {
			t.Fatalf("rotación %d: seq pasó de %d a %d, quiero exactamente +1", i, seqAnterior, seq)
		}
		seqAnterior = seq

		want := p.At(int(seq % n)).Duration
		if until != want {
			t.Fatalf("rotación %d (seq=%d): until = %v, quiero %v (duración de %s)",
				i, seq, until, want, p.At(int(seq%n)).Name)
		}

		elapsed += until
	}
}
