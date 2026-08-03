package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/viewers"
)

// servidorDePrueba levanta el servidor y programa su cierre con t.Cleanup, NO
// con defer.
//
// El orden importa: los defer del test corren ANTES que los t.Cleanup, así que
// un `defer srv.Close()` cerraría el servidor mientras el cuerpo SSE sigue
// abierto —su cierre lo registra abrirSSE, más tarde— y Close() se quedaría
// cinco segundos esperando esa conexión. Con Cleanup el orden se invierte:
// primero el cuerpo, después el servidor, y al final el hub.
func servidorDePrueba(t *testing.T, b *banco) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(b.Handler)
	t.Cleanup(func() { srv.Close() })
	return srv
}

// abrirSSE conecta al endpoint y devuelve un lector de líneas.
func abrirSSE(t *testing.T, srv *httptest.Server, galleta *http.Cookie) (*http.Response, *bufio.Reader) {
	t.Helper()
	r, err := http.NewRequest("GET", srv.URL+"/live/events", nil)
	if err != nil {
		t.Fatalf("armando la petición: %v", err)
	}
	if galleta != nil {
		r.AddCookie(galleta)
	}
	// El timeout cubre TODA la lectura del cuerpo, no sólo la conexión. Sin él,
	// un handler que dejara de mandar eventos colgaría el test para siempre en
	// vez de fallar; con él, la lectura devuelve error y el test lo reporta.
	cliente := srv.Client()
	cliente.Timeout = 5 * time.Second
	resp, err := cliente.Do(r)
	if err != nil {
		t.Fatalf("conectando al SSE: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body)
}

// leerEvento devuelve el primer bloque "data:" que llegue, saltando latidos.
func leerEvento(t *testing.T, br *bufio.Reader) viewers.Evento {
	t.Helper()
	plazo := time.AfterFunc(2*time.Second, func() {
		t.Error("se agotó el plazo esperando un evento del SSE")
	})
	defer plazo.Stop()

	for {
		linea, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("leyendo del SSE: %v", err)
		}
		if !strings.HasPrefix(linea, "data: ") {
			continue // comentario de latido o línea en blanco
		}
		var e viewers.Evento
		if err := json.Unmarshal([]byte(strings.TrimPrefix(linea, "data: ")), &e); err != nil {
			t.Fatalf("el evento no es JSON válido (%q): %v", linea, err)
		}
		return e
	}
}

func TestSSECabecerasYPrimerEvento(t *testing.T) {
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	resp, br := abrirSSE(t, srv, galleta)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, quiero text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, quiero no-cache", cc)
	}

	// El hub entrega el estado vigente al conectar: sin eso el panel quedaría
	// vacío hasta la próxima rotación, que puede tardar 10 segundos.
	e := leerEvento(t, br)
	if e.Espectadores != 1 {
		t.Errorf("Espectadores = %d, quiero 1", e.Espectadores)
	}
}

func TestSSERecibeLasRotaciones(t *testing.T) {
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)
	leerEvento(t, br) // el estado inicial

	// Simula una rotación del motor por la misma vía que usa cmd/server.
	b.Hub.Publicar(viewers.Evento{
		Secuencia:      77,
		Ventana:        []string{"segment7.ts", "segment8.ts", "segment9.ts"},
		ProximaEnMs:    4200,
		Discontinuidad: true,
	})

	e := leerEvento(t, br)
	if e.Secuencia != 77 {
		t.Errorf("Secuencia = %d, quiero 77", e.Secuencia)
	}
	if len(e.Ventana) != 3 || e.Ventana[0] != "segment7.ts" {
		t.Errorf("Ventana = %v", e.Ventana)
	}
	if e.ProximaEnMs != 4200 {
		t.Errorf("ProximaEnMs = %d, quiero 4200", e.ProximaEnMs)
	}
	if !e.Discontinuidad {
		t.Error("Discontinuidad = false, quiero true")
	}
}

func TestSSECuentaDosPestanas(t *testing.T) {
	// El criterio de aceptación textual del bloque: abrir dos pestañas sube el
	// contador a 2, cerrar una lo baja a 1.
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	_, br1 := abrirSSE(t, srv, galleta)
	if e := leerEvento(t, br1); e.Espectadores != 1 {
		t.Fatalf("con una pestaña: Espectadores = %d, quiero 1", e.Espectadores)
	}

	resp2, br2 := abrirSSE(t, srv, galleta)
	if e := leerEvento(t, br2); e.Espectadores != 2 {
		t.Fatalf("con dos pestañas: Espectadores = %d, quiero 2", e.Espectadores)
	}
	// La primera pestaña también se entera, sin recargar nada.
	if e := leerEvento(t, br1); e.Espectadores != 2 {
		t.Errorf("la primera pestaña vio Espectadores = %d, quiero 2", e.Espectadores)
	}

	resp2.Body.Close()
	if e := leerEvento(t, br1); e.Espectadores != 1 {
		t.Errorf("tras cerrar una pestaña: Espectadores = %d, quiero 1", e.Espectadores)
	}
}

func TestSSEDesconexionDesRegistra(t *testing.T) {
	// r.Context().Done() es lo que garantiza que cerrar la pestaña no deje una
	// goroutine colgada esperando eventos para nadie.
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	resp, br := abrirSSE(t, srv, galleta)
	leerEvento(t, br)
	if got := b.Hub.Espectadores(); got != 1 {
		t.Fatalf("Espectadores() = %d, quiero 1", got)
	}

	resp.Body.Close()

	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		if b.Hub.Espectadores() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Espectadores() = %d tras cerrar: el cliente no se dio de baja", b.Hub.Espectadores())
}

func TestSSESinSesionEs401(t *testing.T) {
	b := entorno(t)
	srv := servidorDePrueba(t, b)

	resp, err := srv.Client().Get(srv.URL + "/live/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", resp.StatusCode)
	}
}

func TestSSEElMensajeTerminaEnLineaEnBlanco(t *testing.T) {
	// EventSource sólo DESPACHA el mensaje al ver la línea en blanco. Con un
	// solo \n el navegador acumula datos y no entrega nada: el panel se
	// quedaría vacío para siempre con la suite entera en verde y sin una línea
	// de log. Es la diferencia entre "los tests pasan" y "funciona en el
	// navegador", y no la cubre ninguna otra aserción: leerEvento lee por
	// líneas y un solo \n también termina la línea del data:.
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)

	datos, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("leyendo el evento: %v", err)
	}
	if !strings.HasPrefix(datos, "data: ") {
		t.Fatalf("primera línea = %q, quiero un data:", datos)
	}
	cierre, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("leyendo el cierre del mensaje: %v", err)
	}
	if cierre != "\n" {
		t.Errorf("tras el data: llegó %q, quiero una línea en blanco", cierre)
	}
}

func TestSSEElApagadoDelHubCierraLaConexion(t *testing.T) {
	// El camino real de `docker stop`: se cancela el contexto raíz, el hub
	// cierra los canales de sus clientes y los handlers tienen que volver
	// solos. De eso depende que http.Server.Shutdown termine rápido en vez de
	// agotar su plazo, y el servidor no lleva WriteTimeout que lo rescate.
	//
	// Sin la rama !abierto, un canal cerrado se leería como un evento normal
	// una y otra vez: el handler entraría en un bucle cerrado escribiendo
	// eventos vacíos a toda velocidad.
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	_, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)
	leerEvento(t, br) // el espectador ya está suscrito

	b.Cancelar() // apaga el hub con el espectador todavía conectado

	for {
		linea, err := br.ReadString('\n')
		if err != nil {
			return // EOF: el handler cerró ordenadamente, que es lo que se busca
		}
		if strings.HasPrefix(linea, "data: ") {
			t.Fatalf("el hub apagado sigue emitiendo eventos: %q", linea)
		}
	}
}

func TestSSENoFiltraDatosDelUsuario(t *testing.T) {
	// El evento se difunde a TODOS los espectadores. Si alguna vez alguien
	// agregara el nombre o el email al Evento, todos verían los de todos.
	b := entorno(t)
	srv := servidorDePrueba(t, b)
	u, galleta := usuarioConSesion(t, b)

	_, br := abrirSSE(t, srv, galleta)
	linea, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("leyendo del SSE: %v", err)
	}
	// Sin esto el test pasaría contra una respuesta que no trae ningún evento
	// —un 404, por ejemplo—: no encontrar el secreto en un cuerpo vacío no
	// demuestra nada.
	if !strings.HasPrefix(linea, "data: ") {
		t.Fatalf("la primera línea no es un evento: %q", linea)
	}
	for _, secreto := range []string{u.Email, u.Name} {
		if strings.Contains(linea, secreto) {
			t.Errorf("el evento difundido incluye %q, que es de un usuario concreto", secreto)
		}
	}
}
