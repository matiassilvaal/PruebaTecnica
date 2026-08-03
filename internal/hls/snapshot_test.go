package hls

import (
	"strings"
	"testing"
	"time"
)

func nombresVentana(s *Snapshot) []string {
	out := make([]string, len(s.Window))
	for i, seg := range s.Window {
		out[i] = seg.Name
	}
	return out
}

func iguales(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildSnapshotVentana(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	casos := []struct {
		nombre  string
		seq     int64
		want    []string
		hasDisc bool
	}{
		{"inicio", 0, []string{"segment0.ts", "segment1.ts", "segment2.ts"}, false},
		{"medio", 1, []string{"segment1.ts", "segment2.ts", "segment3.ts"}, false},
		{"antes de la vuelta", 2, []string{"segment2.ts", "segment3.ts", "segment4.ts"}, false},
		// La vuelta cae en la posición 2 de la ventana.
		{"vuelta en posicion 2", 3, []string{"segment3.ts", "segment4.ts", "segment0.ts"}, true},
		// La vuelta cae en la posición 1.
		{"vuelta en posicion 1", 4, []string{"segment4.ts", "segment0.ts", "segment1.ts"}, true},
		// La vuelta cae en la posición 0: la etiqueta ya salió del playlist.
		{"vuelta ya consumida", 5, []string{"segment0.ts", "segment1.ts", "segment2.ts"}, false},
		{"segundo ciclo", 8, []string{"segment3.ts", "segment4.ts", "segment0.ts"}, true},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			s := buildSnapshot(p, c.seq, 3, time.Time{})
			if got := nombresVentana(s); !iguales(got, c.want) {
				t.Errorf("ventana = %v, quiero %v", got, c.want)
			}
			if s.HasDisc != c.hasDisc {
				t.Errorf("HasDisc = %v, quiero %v", s.HasDisc, c.hasDisc)
			}
			if s.Seq != c.seq {
				t.Errorf("Seq = %d, quiero %d", s.Seq, c.seq)
			}
		})
	}
}

func TestBuildSnapshotDiscontinuitySequence(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// DiscSeq cuenta las vueltas completadas antes del primer segmento
	// de la ventana. n = 5.
	casos := []struct{ seq, want int64 }{
		{0, 0}, {4, 0}, {5, 1}, {9, 1}, {10, 2}, {14, 2}, {15, 3},
	}
	for _, c := range casos {
		s := buildSnapshot(p, c.seq, 3, time.Time{})
		if s.DiscSeq != c.want {
			t.Errorf("seq=%d: DiscSeq = %d, quiero %d", c.seq, s.DiscSeq, c.want)
		}
	}
}

func TestBuildSnapshotVentanaSiempreCompleta(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// El enunciado exige 3 segmentos por request, en cualquier posición
	// del ciclo, incluidas las vueltas.
	for seq := int64(0); seq < 40; seq++ {
		s := buildSnapshot(p, seq, 3, time.Time{})
		if len(s.Window) != 3 {
			t.Fatalf("seq=%d: ventana de %d elementos, quiero 3", seq, len(s.Window))
		}
	}
}

func TestBuildSnapshotNextAt(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	momento := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	s := buildSnapshot(p, 0, 3, momento)
	if !s.NextAt.Equal(momento) {
		t.Errorf("NextAt = %v, quiero %v", s.NextAt, momento)
	}
}

func TestRenderPlaylistInicio(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	want := "#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:10\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-DISCONTINUITY-SEQUENCE:0\n" +
		"#EXTINF:10.000000,\n" +
		"segments/segment0.ts\n" +
		"#EXTINF:10.000000,\n" +
		"segments/segment1.ts\n" +
		"#EXTINF:10.000000,\n" +
		"segments/segment2.ts\n"

	got := string(buildSnapshot(p, 0, 3, time.Time{}).Playlist)
	if got != want {
		t.Errorf("playlist:\n%s\nquiero:\n%s", got, want)
	}
}

func TestRenderPlaylistConDiscontinuidad(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// seq=3: la ventana es [s3, s4, s0] y el salto cae antes de s0.
	// s4 dura 4,566667s: el EXTINF debe reflejar la duración real.
	want := "#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:10\n" +
		"#EXT-X-MEDIA-SEQUENCE:3\n" +
		"#EXT-X-DISCONTINUITY-SEQUENCE:0\n" +
		"#EXTINF:10.000000,\n" +
		"segments/segment3.ts\n" +
		"#EXTINF:4.566667,\n" +
		"segments/segment4.ts\n" +
		"#EXT-X-DISCONTINUITY\n" +
		"#EXTINF:10.000000,\n" +
		"segments/segment0.ts\n"

	got := string(buildSnapshot(p, 3, 3, time.Time{}).Playlist)
	if got != want {
		t.Errorf("playlist:\n%s\nquiero:\n%s", got, want)
	}
}

func TestRenderPlaylistNuncaLlevaEndlist(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// La ausencia de ENDLIST es lo que marca el playlist como live.
	// Si apareciera, el player lo trataría como VOD y dejaría de refrescar.
	for seq := int64(0); seq < 20; seq++ {
		got := string(buildSnapshot(p, seq, 3, time.Time{}).Playlist)
		if strings.Contains(got, "EXT-X-ENDLIST") {
			t.Fatalf("seq=%d: el playlist incluye EXT-X-ENDLIST", seq)
		}
	}
}

func TestRenderPlaylistUnaSolaDiscontinuidad(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Con una ventana de 3 sobre un pool de 5 nunca puede haber dos saltos.
	for seq := int64(0); seq < 20; seq++ {
		got := string(buildSnapshot(p, seq, 3, time.Time{}).Playlist)
		if n := strings.Count(got, "#EXT-X-DISCONTINUITY\n"); n > 1 {
			t.Fatalf("seq=%d: %d etiquetas de discontinuidad", seq, n)
		}
	}
}

func TestRenderPlaylistDuracionRealNoTarget(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// El EXTINF del segmento corto debe llevar su duración real. Si el render
	// usara TARGETDURATION para todos, el player consumiría 4,57s de video
	// creyendo que son 10s y se desincronizaría de la ventana.
	got := string(buildSnapshot(p, 4, 3, time.Time{}).Playlist)
	if !strings.Contains(got, "#EXTINF:4.566667,\nsegments/segment4.ts\n") {
		t.Errorf("falta el EXTINF real del segmento corto:\n%s", got)
	}
	if strings.Contains(got, "#EXTINF:10.000000,\nsegments/segment4.ts\n") {
		t.Errorf("el segmento corto salió con duración 10s:\n%s", got)
	}
}
