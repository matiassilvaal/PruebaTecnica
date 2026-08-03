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
