package hls

import (
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

func TestLocateNoDeriva(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Simula 200 rotaciones encadenadas: si el motor incrementara un contador,
	// los errores se acumularían. Derivando del reloj, la posición calculada
	// tras N saltos coincide exactamente con la consulta directa.
	var acumulado time.Duration
	for i := 0; i < 200; i++ {
		_, until := p.Locate(acumulado)
		acumulado += until
	}
	seq, _ := p.Locate(acumulado)
	if seq != 200 {
		t.Errorf("tras 200 rotaciones seq = %d, quiero 200", seq)
	}
}
