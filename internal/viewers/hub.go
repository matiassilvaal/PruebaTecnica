// Package viewers difunde el estado del stream a los espectadores conectados.
//
// No conoce hls ni net/http: recibe eventos ya armados y los reparte. Esa
// ignorancia deliberada es lo que permite probarlo sin levantar un servidor ni
// arrancar el motor.
package viewers

import (
	"context"
	"sync"
	"sync/atomic"
)

// capacidadCliente es el buffer de eventos por espectador.
//
// Ocho es holgado para el ritmo real (un evento cada ~10 s por rotación, más
// los cambios de contador) y sigue siendo un techo duro: 500 espectadores
// lentos ocupan 500*8 eventos, no memoria sin límite.
const capacidadCliente = 8

// capacidadDifusion amortigua ráfagas entre el motor y la goroutine dueña, para
// que un pico corto no se traduzca en eventos descartados.
const capacidadDifusion = 8

// Evento es el estado que ve el panel del player. Se manda completo, no como
// delta: por eso descartarlo ante un cliente lento es inofensivo — el
// siguiente vuelve a traer todo.
//
// Ventana es de SÓLO LECTURA una vez pasada a Publicar. El hub conserva el
// último evento para entregárselo a quien se conecte, así que ese slice sigue
// vivo mucho después de la llamada y lo comparten todos los espectadores:
// mutarlo corrompería lo que ven todos.
type Evento struct {
	Espectadores   int64    `json:"viewers"`
	Secuencia      int64    `json:"sequence"`
	Ventana        []string `json:"window"`
	ProximaEnMs    int64    `json:"nextRotationMs"`
	Discontinuidad bool     `json:"discontinuity"`
}

type cliente struct {
	ch chan Evento

	// listo lo cierra la goroutine dueña cuando terminó de registrar a este
	// cliente y de difundir su alta. Suscribir lo espera, y con eso ofrece una
	// garantía que vale la pena: cuando vuelve, el cliente YA está en el
	// conjunto, Espectadores() ya lo cuenta, y el estado vigente ya está en su
	// canal. Sin esa espera, quien se suscribe corre contra la goroutine dueña
	// y puede leer un contador viejo o encontrarse el evento de alta llegando
	// tarde, detrás de otro que pidió después.
	listo chan struct{}
}

// Hub reparte eventos a los espectadores conectados.
//
// Una SOLA goroutine (Run) es dueña del conjunto de clientes, así que ese mapa
// no lleva mutex: nadie más lo toca. Todo lo demás entra por canales. El único
// atómico es el contador, y existe sólo para que leerlo desde fuera sea barato.
type Hub struct {
	alta     chan *cliente
	baja     chan *cliente
	difundir chan Evento

	// terminado se cierra cuando Run vuelve. Es lo que permite que Suscribir y
	// la función de baja no se cuelguen si el hub ya se apagó.
	terminado chan struct{}
	cerrarUna sync.Once

	espectadores atomic.Int64
}

// NewHub crea el hub. Todavía no reparte nada: hay que arrancar Run en su
// propia goroutine, y exactamente una vez.
func NewHub() *Hub {
	return &Hub{
		alta:      make(chan *cliente),
		baja:      make(chan *cliente),
		difundir:  make(chan Evento, capacidadDifusion),
		terminado: make(chan struct{}),
	}
}

// Espectadores es el número de conexiones vivas. Lectura atómica: la puede
// llamar cualquiera sin coordinarse con la goroutine dueña.
func (h *Hub) Espectadores() int64 { return h.espectadores.Load() }

// Publicar entrega un evento al hub. NUNCA BLOQUEA.
//
// La llama el hook de rotación del motor, que corre síncronamente en la
// goroutine que hace avanzar el stream: si esta función se bloqueara, el
// stream se detendría para todos los espectadores. Ante un hub saturado se
// descarta el evento, que no cuesta nada porque el siguiente trae el estado
// completo otra vez.
func (h *Hub) Publicar(e Evento) {
	select {
	case h.difundir <- e:
	default:
	}
}

// Suscribir registra un espectador y devuelve su canal y su función de baja.
//
// Cuando vuelve, el alta ya está hecha: el cliente está en el conjunto, el
// contador lo incluye y el estado vigente ya está en su canal. Esa sincronía
// cuesta un viaje de ida y vuelta por evento de suscripción —algo que pasa una
// vez por espectador, no por rotación— y a cambio elimina toda una clase de
// carreras entre quien se suscribe y la goroutine dueña.
//
// El canal es de sólo lectura: el que escribe es siempre la goroutine dueña.
// La función de baja es idempotente y segura de llamar aunque el hub ya se
// haya apagado.
func (h *Hub) Suscribir() (<-chan Evento, func()) {
	c := &cliente{
		ch:    make(chan Evento, capacidadCliente),
		listo: make(chan struct{}),
	}

	select {
	case h.alta <- c:
	case <-h.terminado:
		// El hub ya no corre. Devolvemos un canal cerrado en vez de uno vivo:
		// el handler SSE lo lee, ve que está cerrado y termina de inmediato en
		// vez de quedarse esperando eventos que no van a llegar nunca.
		close(c.ch)
		return c.ch, func() {}
	}

	// El alta ya fue aceptada; falta que la goroutine dueña termine de
	// procesarla.
	//
	// Con `alta` sin buffer, que el envío de arriba haya tenido éxito significa
	// que Run ya está dentro del cuerpo del case, y ese cuerpo cierra c.listo
	// sin condiciones: hoy este select siempre sale por la primera rama. La de
	// h.terminado se mantiene porque la garantía depende de que `alta` NO tenga
	// buffer — si alguien se lo agregara, aparecería la ventana en la que Run
	// se apaga con un alta encolada y sin esta rama la espera no volvería nunca.
	select {
	case <-c.listo:
	case <-h.terminado:
	}

	var una sync.Once
	return c.ch, func() {
		una.Do(func() {
			select {
			case h.baja <- c:
			case <-h.terminado:
			}
		})
	}
}

// Run posee el conjunto de clientes hasta que se cancele el contexto.
//
// Debe correr en su propia goroutine y exactamente una vez por Hub: es la
// dueña única de `clientes` y de `ultimo`, y esa exclusividad es lo que
// reemplaza al mutex.
func (h *Hub) Run(ctx context.Context) {
	clientes := make(map[*cliente]struct{})

	// ultimo es el estado más reciente del stream. Se guarda para dárselo al
	// instante a quien se conecta —si no, el panel quedaría vacío hasta la
	// próxima rotación— y para reconstruir el evento cuando lo único que
	// cambió fue el número de espectadores.
	var ultimo Evento

	defer func() {
		// Cerrar los canales hace que los handlers SSE salgan de su bucle solos.
		for c := range clientes {
			close(c.ch)
		}
		h.espectadores.Store(0)
		h.cerrarUna.Do(func() { close(h.terminado) })
	}()

	for {
		select {
		case c := <-h.alta:
			clientes[c] = struct{}{}
			ultimo.Espectadores = int64(len(clientes))
			h.espectadores.Store(ultimo.Espectadores)
			// Una sola difusión, no dos: el recién llegado YA está en el
			// conjunto, así que difundirA le entrega el estado vigente al
			// mismo tiempo que al resto el contador nuevo. Mandárselo aparte
			// además de esto le entregaría el mismo evento dos veces.
			difundirA(clientes, ultimo)
			// Recién ahora Suscribir puede volver: el cliente está registrado,
			// contado y con el estado vigente en su canal.
			close(c.listo)

		case c := <-h.baja:
			// Hoy es inalcanzable: el sync.Once de Suscribir ya garantiza que
			// cada cliente llegue acá una sola vez. Se deja porque es la guarda
			// que hace que el invariante viva en el dueño del estado y no sólo
			// en el llamante: sin ella, cualquier futura vía de baja que no
			// pase por ese Once descontaría de más y cerraría un canal ya
			// cerrado, que es un pánico.
			if _, existe := clientes[c]; !existe {
				continue
			}
			delete(clientes, c)
			close(c.ch)
			ultimo.Espectadores = int64(len(clientes))
			h.espectadores.Store(ultimo.Espectadores)
			difundirA(clientes, ultimo)

		case e := <-h.difundir:
			// El contador lo pone el hub, no el llamante: es el único que lo sabe.
			e.Espectadores = int64(len(clientes))
			ultimo = e
			h.espectadores.Store(e.Espectadores)
			difundirA(clientes, e)

		case <-ctx.Done():
			return
		}
	}
}

// difundirA reparte el evento sin bloquear en ningún cliente.
func difundirA(clientes map[*cliente]struct{}, e Evento) {
	for c := range clientes {
		enviar(c, e)
	}
}

// enviar deposita el evento si hay lugar y lo DESCARTA si no.
//
// Sin el `default`, un solo espectador que dejó de leer —red lenta, pestaña
// congelada— bloquearía a la goroutine dueña y con ella el broadcast a todos
// los demás, mientras los eventos se acumulan sin techo. Es la fuga de memoria
// clásica de este patrón. Descartar es correcto porque cada evento trae el
// estado completo: el siguiente lo pone al día igual.
func enviar(c *cliente, e Evento) {
	select {
	case c.ch <- e:
	default:
	}
}
