package hls

import (
	"context"
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

	// New no dispara el hook (ver TestEngineNewNoDisparaHook...): sólo publica
	// el snapshot inicial en silencio. El hook empieza a contar desde acá.
	if len(recibidos) != 0 {
		t.Fatalf("tras New el hook ya recibió %d snapshots, quiero 0", len(recibidos))
	}

	r.Avanzar(10 * time.Second)
	e.refresh()
	r.Avanzar(10 * time.Second)
	e.refresh()

	if len(recibidos) != 2 {
		t.Fatalf("el hook recibió %d snapshots, quiero 2", len(recibidos))
	}
	for i, want := range []int64{1, 2} {
		if recibidos[i] != want {
			t.Errorf("hook[%d] = %d, quiero %d", i, recibidos[i], want)
		}
	}
}

// TestEngineNewNoDisparaHookRunSiUnaVezPorSecuencia cubre el hallazgo de la
// revisión final: New llamaba a refresh(), que disparaba el hook de forma
// síncrona ANTES de que New devolviera. Con un hub SSE que arranca su goroutine
// (`go hub.Run(ctx)`) recién después de construir el Engine, un Publish sobre
// un canal sin lector todavía deadlockearía el arranque del servidor. Además,
// como Run también llama a refresh() en su primera vuelta, el mismo seq=0 se
// publicaba dos veces.
//
// El fix: New publica el snapshot inicial sin pasar por el hook. El hook
// arranca a dispararse recién con Run, así que hay exactamente un evento por
// rotación, incluida la inicial.
func TestEngineNewNoDisparaHookRunSiUnaVezPorSecuencia(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Segmentos de milisegundos para observar una rotación real sin esperar
	// los 10s del material.
	rapido := newPool([]Segment{
		{Name: "a.ts", Duration: 20 * time.Millisecond},
		{Name: "b.ts", Duration: 20 * time.Millisecond},
		{Name: "c.ts", Duration: 20 * time.Millisecond},
	}, p.dir)

	var mu sync.Mutex
	var recibidos []int64
	e := New(rapido, WithRotationHook(func(s *Snapshot) {
		mu.Lock()
		recibidos = append(recibidos, s.Seq)
		mu.Unlock()
	}))

	mu.Lock()
	trasNew := len(recibidos)
	mu.Unlock()
	if trasNew != 0 {
		t.Fatalf("New disparó el hook %d veces, quiero 0", trasNew)
	}
	if e.Current() == nil {
		t.Fatal("Current() = nil tras New, aunque el hook no se haya disparado")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	time.Sleep(120 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(recibidos) == 0 {
		t.Fatal("Run no disparó el hook en 120ms")
	}
	var vecesSeq0 int
	for _, seq := range recibidos {
		if seq == 0 {
			vecesSeq0++
		}
	}
	if vecesSeq0 != 1 {
		t.Errorf("seq=0 se publicó %d veces por el hook, quiero exactamente 1 (recibidos=%v)", vecesSeq0, recibidos)
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

func TestEngineRunTerminaAlCancelar(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	e := New(p)

	ctx, cancel := context.WithCancel(context.Background())
	listo := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(listo)
	}()

	cancel()
	select {
	case <-listo:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no terminó tras cancelar el contexto")
	}
}

func TestEngineRunRotaConElTiempo(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// Un pool con segmentos de milisegundos permite observar varias rotaciones
	// reales en un test rápido, sin esperar los 10s del material.
	rapido := newPool([]Segment{
		{Name: "a.ts", Duration: 20 * time.Millisecond},
		{Name: "b.ts", Duration: 20 * time.Millisecond},
		{Name: "c.ts", Duration: 20 * time.Millisecond},
		{Name: "d.ts", Duration: 5 * time.Millisecond}, // el caso corto
	}, p.dir)

	e := New(rapido)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	time.Sleep(300 * time.Millisecond)
	got := e.Current().Seq
	// En 300ms con un ciclo de 65ms deben haber pasado varias rotaciones.
	if got < 3 {
		t.Errorf("Seq = %d tras 300ms, esperaba varias rotaciones", got)
	}
}

func TestEngineRunSinCarreras(t *testing.T) {
	p, err := ParseManifest(fixture)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	rapido := newPool([]Segment{
		{Name: "a.ts", Duration: 5 * time.Millisecond},
		{Name: "b.ts", Duration: 5 * time.Millisecond},
		{Name: "c.ts", Duration: 5 * time.Millisecond},
	}, p.dir)

	e := New(rapido)
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if s := e.Current(); s == nil || len(s.Playlist) == 0 {
					t.Error("snapshot inválido mientras Run rota")
					return
				}
			}
		}()
	}
	wg.Wait()
	cancel()
}
