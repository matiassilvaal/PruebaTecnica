package hls

import (
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
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithWindowSize fija cuántos segmentos entrega cada playlist.
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
func WithRotationHook(fn func(*Snapshot)) Option {
	return func(e *Engine) { e.onRotate = fn }
}

// New crea el motor y publica el primer snapshot, de modo que Current() nunca
// devuelve nil aunque Run todavía no haya arrancado.
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
	e.refresh()
	return e
}

// Current devuelve el snapshot vigente. Es seguro desde cualquier goroutine y
// no toma locks.
func (e *Engine) Current() *Snapshot { return e.cur.Load() }

// refresh calcula el snapshot correspondiente al instante actual, lo publica y
// devuelve cuánto falta para la próxima rotación.
//
// Es la costura testeable del motor: los tests la llaman con un reloj falso en
// vez de esperar tiempo real.
func (e *Engine) refresh() time.Duration {
	now := e.now()
	seq, until := e.pool.Locate(now.Sub(e.start))

	snap := buildSnapshot(e.pool, seq, e.windowSize, now.Add(until))
	e.cur.Store(snap)

	if e.onRotate != nil {
		e.onRotate(snap)
	}
	return until
}
