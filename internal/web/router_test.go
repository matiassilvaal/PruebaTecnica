package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSaludResponde200(t *testing.T) {
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
}

func TestSaludFallaSiLaBaseNoResponde(t *testing.T) {
	// Un healthcheck que devuelve 200 pase lo que pase no sirve de nada:
	// Docker reiniciaría contenedores sanos y dejaría vivos los rotos.
	h := NewRouter(Deps{
		Salud: func(context.Context) error { return errors.New("base caída") },
		Log:   log.New(os.Stderr, "test: ", 0),
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("código = %d, quiero 503", w.Code)
	}
}

func TestEstaticosSeSirvenEmbebidos(t *testing.T) {
	// Van dentro del binario: el contenedor no necesita copiarlos ni depender
	// de cuál sea el directorio de trabajo.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/app.css", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, quiero un max-age", cc)
	}
}

func TestEstaticosNoRequierenSesion(t *testing.T) {
	// El CSS de la página de login tiene que cargar sin sesión.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/app.css", nil))
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusFound {
		t.Fatalf("código = %d: los estáticos no deben estar protegidos", w.Code)
	}
}

func TestRecuperarDevuelve500YNoTumbaElProceso(t *testing.T) {
	// Un pánico en un handler no puede llevarse por delante al resto del
	// servidor: el motor del stream y el hub corren en este mismo proceso, así
	// que caerse significa cortarle el stream a todos los espectadores.
	registro := &bufferDeLog{}
	entraEnPanico := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("explotó a propósito")
	})
	h := recuperar(log.New(registro, "", 0), entraEnPanico)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/lo-que-sea", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d, quiero 500", w.Code)
	}
	if !strings.Contains(registro.String(), "explotó a propósito") {
		t.Errorf("el log no menciona el pánico: %q", registro.String())
	}
}

func TestRecuperarDejaPasarErrAbortHandler(t *testing.T) {
	// net/http usa ErrAbortHandler para abortar una respuesta a propósito, y
	// lo emite cada vez que un espectador cierra la pestaña con el SSE abierto.
	// Tratarlo como error llenaría el log de ruido; el servidor ya lo maneja.
	registro := &bufferDeLog{}
	aborta := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := recuperar(log.New(registro, "", 0), aborta)

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recuperar() se tragó ErrAbortHandler: recover() = %v", v)
		}
		if registro.String() != "" {
			t.Errorf("no debe registrarse nada, se registró: %q", registro.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/sse", nil))
	t.Fatal("quiero que el pánico se propague")
}

func TestRegistrarDejaLineaDeLog(t *testing.T) {
	registro := &bufferDeLog{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := registrar(log.New(registro, "", 0), ok)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/ruta/observada", nil))

	linea := registro.String()
	if !strings.Contains(linea, "/ruta/observada") {
		t.Errorf("el log no menciona la ruta: %q", linea)
	}
	if !strings.Contains(linea, "418") {
		t.Errorf("el log no menciona el código de estado: %q", linea)
	}
}

func TestRespuestaObservadaConservaElFlusher(t *testing.T) {
	// El SSE hace una aserción a http.Flusher sobre el ResponseWriter. Si el
	// envoltorio del logging no reexpusiera Flush, esa aserción fallaría y el
	// endpoint devolvería 500 en producción, donde los middlewares sí están
	// puestos — pero pasaría los tests del handler aislado. Este test cierra
	// justo ese hueco.
	var vioFlusher bool
	interior := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, vioFlusher = w.(http.Flusher)
	})
	h := registrar(log.New(&bufferDeLog{}, "", 0), interior)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/live/events", nil))

	if !vioFlusher {
		t.Fatal("el writer envuelto no implementa http.Flusher: el SSE no podría vaciar")
	}
}
