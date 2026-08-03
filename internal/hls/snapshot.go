package hls

import (
	"bytes"
	"fmt"
	"time"
)

// Snapshot es el estado del stream en un instante. Es INMUTABLE: una vez
// construido no se modifica nunca. El motor publica snapshots nuevos en cada
// rotación y descarta el anterior, en vez de mutar un estado compartido. Eso
// permite que los lectores accedan sin locks.
type Snapshot struct {
	Seq      int64     // EXT-X-MEDIA-SEQUENCE
	DiscSeq  int64     // EXT-X-DISCONTINUITY-SEQUENCE
	HasDisc  bool      // hay una discontinuidad dentro de esta ventana
	Window   []Segment // exactamente windowSize elementos
	Playlist []byte    // el .m3u8 ya renderizado
	NextAt   time.Time // instante de la próxima rotación
}

// buildSnapshot arma la ventana que arranca en `seq` y renderiza su playlist.
//
// La discontinuidad se marca cuando el ciclo vuelve al primer segmento del
// pool, porque ahí los timestamps del video saltan hacia atrás y el
// decodificador debe reinicializarse. Sólo se emite la etiqueta si el salto
// cae DENTRO de la ventana (posición 1 en adelante): si cayera en la posición
// 0, la etiqueta ya salió del playlist y quedó contabilizada en DiscSeq.
func buildSnapshot(p *Pool, seq int64, windowSize int, nextAt time.Time) *Snapshot {
	n := int64(p.Len())

	s := &Snapshot{
		Seq:     seq,
		DiscSeq: seq / n,
		Window:  make([]Segment, 0, windowSize),
		NextAt:  nextAt,
	}

	discAt := -1 // posición de la ventana que lleva la etiqueta, o -1
	for k := 0; k < windowSize; k++ {
		pos := seq + int64(k)
		s.Window = append(s.Window, p.At(int(pos%n)))
		if k > 0 && pos%n == 0 {
			discAt = k
			s.HasDisc = true
		}
	}

	s.Playlist = renderPlaylist(p, s, discAt)
	return s
}

// renderPlaylist genera el .m3u8 una sola vez, al construir el snapshot.
//
// Como el playlist sólo cambia una vez por rotación, generarlo por request
// sería repetir el mismo trabajo N veces. Renderizándolo acá, cada petición
// HTTP se reduce a escribir bytes ya listos: sin formateo ni asignaciones.
//
// discAt es la posición de la ventana que debe llevar la etiqueta de
// discontinuidad, o -1 si no hay ninguna.
func renderPlaylist(p *Pool, s *Snapshot, discAt int) []byte {
	var b bytes.Buffer
	b.Grow(512)

	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n", p.Target())
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", s.Seq)
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", s.DiscSeq)

	for k, seg := range s.Window {
		if k == discAt {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\nsegments/%s\n", seg.Duration.Seconds(), seg.Name)
	}

	// Sin EXT-X-ENDLIST a propósito: su ausencia es lo que le dice al player
	// que el stream sigue vivo y que debe volver a pedir el playlist.
	return b.Bytes()
}
