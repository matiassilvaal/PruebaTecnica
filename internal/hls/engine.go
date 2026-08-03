package hls

import (
	"context"
	"sync/atomic"
	"time"
)

const defaultWindowSize = 3

// Engine mantiene el estado vigente del livestream y lo publica de forma
// atómica.
//
// El estado no se muta: en cada rotación se construye un Snapshot nuevo y se
// reemplaza el puntero. Los lectores hacen Load, que es wait-free — un
// espectador o diez mil cuestan lo mismo y no hay contención de lock.
type Engine struct {
	pool       *Pool
	windowSize int
	now        func() time.Time
	start      time.Time
	cur        atomic.Pointer[Snapshot]
	onRotate   func(*Snapshot)
}

// Option configura el Engine en New.
type Option func(*Engine)

// WithClock inyecta el reloj. Los tests lo usan para avanzar el tiempo sin
// esperar; en producción queda time.Now.
//
// Sólo gobierna el cálculo de refresh(): Run duerme sobre el reloj real
// (time.NewTimer), nunca sobre este reloj inyectado. Un reloj falso sirve
// para probar refresh() de forma determinista y sin esperar, pero NO sirve
// para conducir Run — con un reloj congelado, Run igual despertaría cada
// `until` de tiempo real y recalcularía una y otra vez el mismo `seq`, sin
// ningún error visible.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithWindowSize fija cuántos segmentos entrega cada playlist.
//
// Los valores n<=0 se ignoran silenciosamente y el motor conserva el default
// de 3: es una guarda defensiva, no un error reportado al llamador.
func WithWindowSize(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.windowSize = n
		}
	}
}

// WithRotationHook registra una función que se llama con cada snapshot nuevo.
// La usa el hub SSE para avisar al frontend, sin que este paquete tenga que
// conocerlo.
//
// El hook corre SÍNCRONAMENTE en la goroutine de rotación (la misma que
// ejecuta Run): no debe bloquear ni entrar en pánico, porque ambos efectos se
// propagan directo al motor y detienen el avance del stream. Empieza a
// dispararse cuando arranca Run — New publica el snapshot inicial sin
// llamarlo, así que New nunca puede colgarse por un consumidor lento o
// ausente, y no se emiten dos eventos para la secuencia inicial.
func WithRotationHook(fn func(*Snapshot)) Option {
	return func(e *Engine) { e.onRotate = fn }
}

// New crea el motor y publica el primer snapshot, de modo que Current() nunca
// devuelve nil aunque Run todavía no haya arrancado.
//
// New NO dispara el hook de rotación: sólo publica el estado. El hook
// registrado con WithRotationHook empieza a llamarse recién cuando arranca
// Run, para que New no pueda bloquearse esperando a un consumidor y para que
// no se publiquen dos eventos con el mismo `seq` inicial (uno desde New y
// otro desde la primera vuelta de Run).
func New(p *Pool, opts ...Option) *Engine {
	e := &Engine{
		pool:       p,
		windowSize: defaultWindowSize,
		now:        time.Now,
	}
	for _, o := range opts {
		o(e)
	}
	e.start = e.now()
	snap, _ := e.snapshotNow()
	e.cur.Store(snap)
	return e
}

// Current devuelve el snapshot vigente. Es seguro desde cualquier goroutine y
// no toma locks.
func (e *Engine) Current() *Snapshot { return e.cur.Load() }

// snapshotNow calcula el snapshot correspondiente al instante actual del
// reloj inyectado, sin publicarlo ni disparar el hook. Es la lógica pura que
// comparten New (que publica sin hook) y refresh (que publica y dispara el
// hook).
func (e *Engine) snapshotNow() (snap *Snapshot, until time.Duration) {
	now := e.now()
	seq, until := e.pool.Locate(now.Sub(e.start))
	return buildSnapshot(e.pool, seq, e.windowSize, now.Add(until)), until
}

// refresh calcula el snapshot correspondiente al instante actual, lo publica,
// dispara el hook de rotación si hay uno registrado, y devuelve cuánto falta
// para la próxima rotación.
//
// Es la costura testeable del motor: los tests la llaman con un reloj falso en
// vez de esperar tiempo real. Sólo debe llamarla Run (más los propios tests):
// es la única vía por la que el hook se dispara.
func (e *Engine) refresh() time.Duration {
	snap, until := e.snapshotNow()
	e.cur.Store(snap)

	if e.onRotate != nil {
		e.onRotate(snap)
	}
	return until
}

// Run mantiene el stream avanzando hasta que se cancele el contexto.
//
// Cada espera se calcula contra el instante de inicio absoluto, no sumando
// intervalos: si una iteración se atrasa, la siguiente se re-ancla al reloj en
// vez de arrastrar el error. Por eso el bucle no puede derivar.
//
// Run debe llamarse exactamente una vez por Engine. La garantía de un solo
// escritor sobre el estado del motor (start, cur) depende de que sólo esta
// goroutine llame a refresh(); es una convención del llamador, no algo
// forzado en código.
func (e *Engine) Run(ctx context.Context) {
	for {
		until := e.refresh()

		timer := time.NewTimer(until)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}
