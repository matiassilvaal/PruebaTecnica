package hls

import (
	"sync"
	"testing"
	"time"
)

// relojFalso permite avanzar el tiempo a voluntad. Sin esto, probar la vuelta
// del ciclo exigiría esperar minutos reales.
type relojFalso struct {
	mu    sync.Mutex
	ahora time.Time
}

func nuevoReloj() *relojFalso {
	return &relojFalso{ahora: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)}
}

func (r *relojFalso) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ahora
}

func (r *relojFalso) Avanzar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ahora = r.ahora.Add(d)
}

func motorDePrueba(t *testing.T) (*Engine, *relojFalso) {
	t.Helper()
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	r := nuevoReloj()
	return New(p, WithClock(r.Now)), r
}

func TestEngineSnapshotInicial(t *testing.T) {
	e, _ := motorDePrueba(t)
	s := e.Current()
	if s == nil {
		t.Fatal("Current() = nil justo después de New")
	}
	if s.Seq != 0 {
		t.Errorf("Seq inicial = %d, quiero 0", s.Seq)
	}
	if len(s.Window) != 3 {
		t.Errorf("ventana de %d, quiero 3", len(s.Window))
	}
}

func TestEngineRefreshAvanzaConElReloj(t *testing.T) {
	e, r := motorDePrueba(t)

	r.Avanzar(10 * time.Second)
	e.refresh()
	if got := e.Current().Seq; got != 1 {
		t.Errorf("tras 10s Seq = %d, quiero 1", got)
	}

	r.Avanzar(30 * time.Second) // total 40s: entra el segmento corto
	until := e.refresh()
	if got := e.Current().Seq; got != 4 {
		t.Errorf("tras 40s Seq = %d, quiero 4", got)
	}
	want := 4566667 * time.Microsecond
	if until != want {
		t.Errorf("until = %v, quiero %v (la rotación corta)", until, want)
	}
}

func TestEngineVueltaDeCiclo(t *testing.T) {
	e, r := motorDePrueba(t)
	p, _ := ParseManifest(fixture)

	r.Avanzar(p.Total())
	e.refresh()
	s := e.Current()
	if s.Seq != 5 {
		t.Errorf("tras una vuelta Seq = %d, quiero 5", s.Seq)
	}
	if s.DiscSeq != 1 {
		t.Errorf("DiscSeq = %d, quiero 1", s.DiscSeq)
	}
}

func TestEngineNextAt(t *testing.T) {
	e, r := motorDePrueba(t)
	inicio := r.Now()
	s := e.Current()
	if !s.NextAt.Equal(inicio.Add(10 * time.Second)) {
		t.Errorf("NextAt = %v, quiero %v", s.NextAt, inicio.Add(10*time.Second))
	}
}

func TestEngineRotationHook(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	r := nuevoReloj()
	var recibidos []int64
	e := New(p, WithClock(r.Now), WithRotationHook(func(s *Snapshot) {
		recibidos = append(recibidos, s.Seq)
	}))

	r.Avanzar(10 * time.Second)
	e.refresh()
	r.Avanzar(10 * time.Second)
	e.refresh()

	// New publica el snapshot inicial, más dos refresh explícitos.
	if len(recibidos) != 3 {
		t.Fatalf("el hook recibió %d snapshots, quiero 3", len(recibidos))
	}
	for i, want := range []int64{0, 1, 2} {
		if recibidos[i] != want {
			t.Errorf("hook[%d] = %d, quiero %d", i, recibidos[i], want)
		}
	}
}

func TestEngineWindowSize(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	e := New(p, WithWindowSize(4))
	if got := len(e.Current().Window); got != 4 {
		t.Errorf("ventana de %d, quiero 4", got)
	}
}

func TestEngineLecturaConcurrente(t *testing.T) {
	e, r := motorDePrueba(t)

	// Muchos lectores mientras el escritor rota: con -race esto detecta
	// cualquier acceso no sincronizado al estado del stream.
	var wg sync.WaitGroup
	fin := make(chan struct{})

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-fin:
					return
				default:
					s := e.Current()
					if s == nil || len(s.Window) != 3 || len(s.Playlist) == 0 {
						t.Error("snapshot inconsistente durante lectura concurrente")
						return
					}
				}
			}
		}()
	}

	for i := 0; i < 100; i++ {
		r.Avanzar(10 * time.Second)
		e.refresh()
	}
	close(fin)
	wg.Wait()
}
