package viewers

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// esperarA reintenta hasta que la condición se cumple o se agota el plazo.
//
// Suscribir es síncrono —cuando vuelve, el alta ya está hecha— pero la baja y
// la difusión no lo son: afirmar sobre ellas inmediatamente sería una carrera
// contra la goroutine dueña.
func esperarA(t *testing.T, motivo string, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(time.Second)
	for time.Now().Before(limite) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("se agotó el plazo esperando: %s", motivo)
}

func TestHubCuentaEspectadores(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	if got := h.Espectadores(); got != 0 {
		t.Fatalf("Espectadores() inicial = %d, quiero 0", got)
	}

	_, salir1 := h.Suscribir()
	esperarA(t, "un espectador", func() bool { return h.Espectadores() == 1 })

	_, salir2 := h.Suscribir()
	esperarA(t, "dos espectadores", func() bool { return h.Espectadores() == 2 })

	salir1()
	esperarA(t, "vuelta a un espectador", func() bool { return h.Espectadores() == 1 })

	salir2()
	esperarA(t, "vuelta a cero", func() bool { return h.Espectadores() == 0 })
}

func TestHubDifundeATodos(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	a, salirA := h.Suscribir()
	defer salirA()
	b, salirB := h.Suscribir()
	defer salirB()

	// Cada suscripción provoca un evento de "cambió el contador"; los
	// vaciamos para que el siguiente Publicar sea inequívoco.
	drenar(a)
	drenar(b)

	h.Publicar(Evento{Secuencia: 42, Ventana: []string{"segment0.ts"}})

	for nombre, ch := range map[string]<-chan Evento{"a": a, "b": b} {
		select {
		case e := <-ch:
			if e.Secuencia != 42 {
				t.Errorf("%s: Secuencia = %d, quiero 42", nombre, e.Secuencia)
			}
			if e.Espectadores != 2 {
				t.Errorf("%s: Espectadores = %d, quiero 2: el hub completa el contador", nombre, e.Espectadores)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s no recibió el evento", nombre)
		}
	}
}

func TestHubClienteLentoNoBloqueaALosDemas(t *testing.T) {
	// El test que respalda la afirmación sobre manejo de RAM: un cliente que
	// deja de leer no puede congelar el broadcast ni acumular eventos sin
	// límite. Si el envío no tuviera `default`, este test colgaría.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	// Sin defer de las funciones de baja a propósito: el `defer cancelar()` de
	// arriba ya cierra los canales al apagar el hub. Si además se difiriera la
	// baja, un t.Fatal en este test la ejecutaría ANTES que cancelar (los defer
	// corren en orden inverso) y quedaría bloqueada mandando a un Run detenido:
	// el fallo se reportaría como un cuelgue de diez minutos en vez de una
	// línea FAIL.
	lento, _ := h.Suscribir() // nunca se lee: es el cliente atascado
	rapido, _ := h.Suscribir()

	// El cliente rápido necesita un lector ACTIVO. Sin él su buffer se llenaría
	// igual que el del lento, y el test no distinguiría "el hub descartó por
	// lento" de "el hub se detuvo" — que es justamente lo que viene a separar.
	var ultimoVisto atomic.Int64
	go func() {
		for e := range rapido {
			ultimoVisto.Store(e.Secuencia)
		}
	}()

	// Muchos más eventos que la capacidad del buffer por cliente.
	const n = 500
	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < n; i++ {
			h.Publicar(Evento{Secuencia: int64(i)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(2 * time.Second):
		t.Fatal("Publicar se bloqueó: un cliente lento está frenando al motor")
	}

	// El buffer del lento está acotado: no puede haber tragado los 500.
	if l := len(lento); l > capacidadCliente {
		t.Errorf("el cliente lento acumuló %d eventos, el tope es %d", l, capacidadCliente)
	}

	// Y el hub sigue aceptando y entregando. Se republica en cada intento
	// porque Publicar descarta cuando el canal de difusión está lleno, y tras
	// una ráfaga de 500 puede estarlo: un único intento haría el test
	// intermitente por la razón equivocada.
	esperarA(t, "que el cliente rápido siga recibiendo", func() bool {
		h.Publicar(Evento{Secuencia: 9999})
		return ultimoVisto.Load() == 9999
	})
}

func TestHubDesconexionNoDejaGoroutines(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()

	// Run arranca envuelto en un canal propio para poder afirmar sobre ESTA
	// goroutine. runtime.NumGoroutine() es global al proceso: los tests
	// anteriores dejan sus Run drenando, así que un descenso del contador
	// puede venir de una goroutine ajena terminando mientras esta sigue viva.
	// Con `corriendo` la comprobación es directa y no admite esa confusión.
	corriendo := make(chan struct{})
	go func() {
		defer close(corriendo)
		h.Run(ctx)
	}()
	esperarA(t, "que el hub arranque", func() bool { return h.Espectadores() == 0 })

	// Alta y baja repetidas no deben acumular nada. Acá NumGoroutine() sí
	// sirve: nada en este bucle crea goroutines, así que un aumento sólo puede
	// venir del hub.
	base := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		_, salir := h.Suscribir()
		salir()
	}
	esperarA(t, "que se den de baja todos", func() bool { return h.Espectadores() == 0 })
	if n := runtime.NumGoroutine(); n > base {
		t.Errorf("suscribir y dar de baja dejó goroutines: %d, empezó en %d", n, base)
	}

	cancelar()
	select {
	case <-corriendo:
	case <-time.After(time.Second):
		t.Fatal("Run no volvió tras cancelar el contexto")
	}
}

func TestApagarCierraLosCanalesDeLosClientes(t *testing.T) {
	// Es lo que hace que los handlers SSE salgan de su bucle solos al apagar el
	// proceso. Sin esto, cada espectador conectado dejaría una goroutine
	// esperando eventos que ya no van a llegar, y http.Server.Shutdown agotaría
	// su plazo completo esperando conexiones que no se cierran nunca.
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()
	go h.Run(ctx)

	// Deliberadamente SIN llamar a salir(): lo que se prueba es que el apagado
	// cierra el canal por su cuenta. Si el test se diera de baja primero, el
	// canal lo cerraría la rama de baja y esta comprobación no probaría nada.
	ch, _ := h.Suscribir()
	cancelar()

	plazo := time.After(time.Second)
	for {
		select {
		case _, abierto := <-ch:
			if !abierto {
				return // el canal se cerró: es exactamente lo que se busca
			}
			// Eventos pendientes en el buffer: seguir drenando hasta el cierre.
		case <-plazo:
			t.Fatal("el canal del cliente no se cerró al apagar el hub: el handler SSE quedaría colgado")
		}
	}
}

func TestPublicarNoBloqueaConElHubDetenido(t *testing.T) {
	// LA garantía que sostiene todo el diseño: Publicar lo llama el hook de
	// rotación del motor, síncronamente en la goroutine que hace avanzar el
	// stream. Si se bloqueara, el stream se detendría para TODOS.
	//
	// Se prueba en aislamiento y sin arrancar Run: así nadie drena `difundir`,
	// y basta con superar su capacidad para que el select/default sea lo único
	// que separa esta función de un bloqueo permanente. Probarlo con Run
	// corriendo no serviría — el hub vacía el canal en microsegundos y un
	// Publicar bloqueante pasaría igual.
	h := NewHub() // Run nunca se llama

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < capacidadDifusion*10; i++ {
			h.Publicar(Evento{Secuencia: int64(i)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(time.Second):
		t.Fatal("Publicar se bloqueó con el hub detenido: eso frenaría la goroutine de rotación del motor")
	}
}

func TestSalirEsIdempotente(t *testing.T) {
	// El handler SSE llama a salir() con defer, pero también podría llamarlo
	// en una rama de error: dos veces no debe descontar dos espectadores.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	_, salir := h.Suscribir()
	_, quedarse := h.Suscribir()
	defer quedarse()
	esperarA(t, "dos espectadores", func() bool { return h.Espectadores() == 2 })

	salir()
	salir()
	salir()
	esperarA(t, "un espectador", func() bool { return h.Espectadores() == 1 })

	// Un tercer valor distinto de 1 en el próximo medio segundo sería una baja
	// de más colándose tarde.
	time.Sleep(100 * time.Millisecond)
	if got := h.Espectadores(); got != 1 {
		t.Fatalf("Espectadores() = %d tras tres salir(), quiero 1", got)
	}
}

func TestSuscribirTrasApagarNoCuelga(t *testing.T) {
	// Un request SSE puede llegar entre que el contexto se cancela y que el
	// servidor HTTP termina de cerrar. Suscribir no puede quedarse colgado
	// escribiendo en un canal que ya nadie lee.
	ctx, cancelar := context.WithCancel(context.Background())
	h := NewHub()
	go h.Run(ctx)
	cancelar()
	esperarA(t, "que Run termine", func() bool {
		select {
		case <-h.terminado:
			return true
		default:
			return false
		}
	})

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		ch, salir := h.Suscribir()
		salir()
		// El canal ya viene cerrado: leerlo devuelve el cero inmediatamente.
		<-ch
	}()
	select {
	case <-hecho:
	case <-time.After(time.Second):
		t.Fatal("Suscribir se colgó con el hub apagado")
	}
}

func TestHubEntregaElUltimoEstadoAlConectar(t *testing.T) {
	// Sin esto, un espectador que abre la página justo después de una rotación
	// vería el panel vacío hasta 10 segundos.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	primero, salirPrimero := h.Suscribir()
	defer salirPrimero()
	drenar(primero)
	h.Publicar(Evento{Secuencia: 7, Ventana: []string{"segment7.ts"}})
	select {
	case <-primero:
	case <-time.After(time.Second):
		t.Fatal("el primer cliente no recibió el evento")
	}

	segundo, salirSegundo := h.Suscribir()
	defer salirSegundo()
	select {
	case e := <-segundo:
		if e.Secuencia != 7 {
			t.Errorf("Secuencia = %d, quiero 7: el hub debe entregar el último estado al conectar", e.Secuencia)
		}
		if e.Espectadores != 2 {
			t.Errorf("Espectadores = %d, quiero 2", e.Espectadores)
		}
	case <-time.After(time.Second):
		t.Fatal("el cliente nuevo no recibió el estado vigente")
	}
}

func TestElReenvioNoEntregaUnaCuentaRegresivaVieja(t *testing.T) {
	// El bug que este test cierra sólo se ve en el navegador, que es donde no
	// hay tests: el hub redifunde el último estado en cada alta y cada baja, así
	// que con la cuenta regresiva calculada al publicar, abrir una segunda
	// pestaña le mandaba a TODOS los espectadores el plazo del momento de la
	// rotación. El contador saltaba hacia atrás —de "3.0 s" a "10.0 s"— y la
	// barra volvía a cero, justo en el escenario que el enunciado usa de demo.
	// Y el que recién se suscribía arrancaba con hasta una rotación de más.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	primero, salirPrimero := h.Suscribir()
	defer salirPrimero()
	drenar(primero)

	h.Publicar(Evento{Secuencia: 3, ProximaEn: time.Now().Add(500 * time.Millisecond)})

	var alPublicar int64
	select {
	case e := <-primero:
		alPublicar = e.ProximaEnMs
	case <-time.After(time.Second):
		t.Fatal("el primer cliente no recibió el evento publicado")
	}
	if alPublicar <= 0 {
		t.Fatalf("ProximaEnMs = %d al publicar, quiero un plazo positivo", alPublicar)
	}

	// Suficiente para que un valor precomputado se note viejo y muy por debajo
	// del segundo que ningún test puede tardar.
	time.Sleep(100 * time.Millisecond)

	// El alta del segundo cliente es lo que dispara el reenvío del último
	// estado a todo el conjunto.
	segundo, salirSegundo := h.Suscribir()
	defer salirSegundo()

	select {
	case e := <-segundo:
		if e.ProximaEnMs >= alPublicar {
			t.Errorf("el que se suscribe recibe ProximaEnMs = %d, quiero menos de %d: le llega el plazo de la rotación y no el que queda", e.ProximaEnMs, alPublicar)
		}
	case <-time.After(time.Second):
		t.Fatal("el cliente nuevo no recibió el estado vigente")
	}

	select {
	case e := <-primero:
		if e.ProximaEnMs >= alPublicar {
			t.Errorf("el que ya estaba conectado recibe ProximaEnMs = %d, quiero menos de %d: su contador saltaría hacia atrás", e.ProximaEnMs, alPublicar)
		}
	case <-time.After(time.Second):
		t.Fatal("el cliente ya conectado no recibió el reenvío del alta")
	}
}

func TestSinRotacionLaCuentaRegresivaEsCero(t *testing.T) {
	// El estado inicial —antes de la primera rotación— no tiene instante al que
	// apuntar. Derivar los milisegundos de un time.Time cero daría un número
	// enorme y negativo, y el panel lo leería como una rotación vencida hace
	// siglos en vez de como "todavía no hay referencia".
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := NewHub()
	go h.Run(ctx)

	ch, salir := h.Suscribir()
	defer salir()

	select {
	case e := <-ch:
		if e.ProximaEnMs != 0 {
			t.Errorf("ProximaEnMs = %d sin rotación previa, quiero 0", e.ProximaEnMs)
		}
	case <-time.After(time.Second):
		t.Fatal("el cliente no recibió el evento de su alta")
	}
}

func drenar(ch <-chan Evento) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
