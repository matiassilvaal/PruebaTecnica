package web

import (
	"context"
	"errors"
	"io"
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
	// no-cache no es "no guardar": es "guardá pero preguntá antes de usar".
	// Un max-age sin ETag dejaba al navegador con la copia vieja hasta que
	// expirara, sin manera de preguntar si había cambiado.
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, quiero \"no-cache\"", cc)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("sin ETag no hay forma de revalidar: embed.FS tampoco da Last-Modified")
	}
}

func TestEstaticosRevalidanCon304(t *testing.T) {
	// El arreglo de un defecto del player quedó invisible una hora para quien
	// ya había abierto la página: los estáticos se servían con max-age y sin
	// ETag ni Last-Modified —embed.FS reporta ModTime cero, así que
	// http.ServeContent omite Last-Modified— y el navegador no tenía con qué
	// preguntar si el archivo había cambiado.
	//
	// Este test fija las dos mitades del arreglo: que se emita un ETag, y que
	// un If-None-Match con ese valor devuelva 304 sin cuerpo. Sin la segunda,
	// el ETag sería decorativo y hls.js (543 KB) viajaría entero en cada carga.
	b := entorno(t)

	primera := httptest.NewRecorder()
	b.Handler.ServeHTTP(primera, httptest.NewRequest("GET", "/static/player.js", nil))
	etag := primera.Header().Get("ETag")
	if etag == "" {
		t.Fatal("la primera respuesta no trae ETag")
	}
	if primera.Body.Len() == 0 {
		t.Fatal("la primera respuesta llegó vacía")
	}

	r := httptest.NewRequest("GET", "/static/player.js", nil)
	r.Header.Set("If-None-Match", etag)
	segunda := httptest.NewRecorder()
	b.Handler.ServeHTTP(segunda, r)

	if segunda.Code != http.StatusNotModified {
		t.Fatalf("código = %d, quiero 304", segunda.Code)
	}
	if segunda.Body.Len() != 0 {
		t.Errorf("un 304 no lleva cuerpo, llegaron %d bytes", segunda.Body.Len())
	}

	// Y un ETag que no corresponde tiene que traer el archivo de nuevo: si no,
	// el navegador se quedaría con una versión vieja para siempre.
	otro := httptest.NewRequest("GET", "/static/player.js", nil)
	otro.Header.Set("If-None-Match", `"0000000000000000"`)
	tercera := httptest.NewRecorder()
	b.Handler.ServeHTTP(tercera, otro)

	if tercera.Code != http.StatusOK || tercera.Body.Len() == 0 {
		t.Errorf("con un ETag distinto: código = %d, %d bytes; quiero 200 con cuerpo",
			tercera.Code, tercera.Body.Len())
	}
}

func TestElEtagCambiaConElContenido(t *testing.T) {
	// Dos archivos distintos no pueden compartir huella, o desplegar un cambio
	// en uno haría que el navegador siguiera usando la versión vieja del otro.
	css := etagsEstaticos["/static/app.css"]
	js := etagsEstaticos["/static/player.js"]

	if css == "" || js == "" {
		t.Fatalf("faltan ETags: css=%q js=%q", css, js)
	}
	if css == js {
		t.Errorf("app.css y player.js comparten ETag (%s): la huella no depende del contenido", css)
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

func TestRegistrarUsa200CuandoElHandlerNoLoFija(t *testing.T) {
	// Un handler que sólo llama a Write —como /healthz en su camino sano, que
	// es la petición más frecuente del sistema— nunca pasa por WriteHeader.
	// Sin el valor inicial de respuestaObservada, todas esas peticiones
	// quedarían registradas con un 0.
	registro := &bufferDeLog{}
	soloEscribe := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	h := registrar(log.New(registro, "", 0), soloEscribe)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/solo-write", nil))

	if !strings.Contains(registro.String(), "200") {
		t.Errorf("el log no registró 200 para un handler que sólo escribe: %q", registro.String())
	}
}

func TestElPanicoIgualDejaLineaDeLog(t *testing.T) {
	// El orden de los middlewares es load-bearing: `registrar` tiene que
	// envolver a `recuperar`, no al revés. Invertido, el pánico atravesaría a
	// registrar antes de llegar a su Printf, y la petición que tumbó al handler
	// sería justamente la única sin línea de log — la que más falta hace.
	//
	// Se prueba sobre el router REAL, no sobre una composición armada acá: una
	// composición local seguiría pasando aunque NewRouter invirtiera el orden.
	// El pánico entra por la función de salud, que es la única vía que Deps
	// ofrece para meterlo dentro de una ruta de verdad.
	registro := &bufferDeLog{}
	h := NewRouter(Deps{
		Salud: func(context.Context) error { panic("explotó dentro de una ruta real") },
		Log:   log.New(registro, "", 0),
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("código = %d, quiero 500", w.Code)
	}
	linea := registro.String()
	if !strings.Contains(linea, "/healthz") {
		t.Errorf("no quedó línea de log para la petición que entró en pánico: %q", linea)
	}
	if !strings.Contains(linea, "500") {
		t.Errorf("la línea de log no registra el 500: %q", linea)
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

func TestRespuestaObservadaConservaElReaderFrom(t *testing.T) {
	// Mismo hueco que el del Flusher, con un costo distinto: http.ServeContent
	// pregunta por io.ReaderFrom para mandar el .ts sin copiarlo por el espacio
	// de usuario. Envuelto y sin reexponerlo, cada segmento pasaría por un
	// buffer intermedio de 32 KB — invisible en los tests del handler aislado y
	// pagado en producción, que es donde los middlewares están puestos.
	var vioReaderFrom bool
	var copiado int64
	interior := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rf, ok := w.(io.ReaderFrom)
		vioReaderFrom = ok
		if !ok {
			return
		}
		n, err := rf.ReadFrom(strings.NewReader("contenido del segmento"))
		if err != nil {
			t.Errorf("ReadFrom: %v", err)
		}
		copiado = n
	})
	h := registrar(log.New(&bufferDeLog{}, "", 0), interior)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/live/segments/segment0.ts", nil))

	if !vioReaderFrom {
		t.Fatal("el writer envuelto no implementa io.ReaderFrom: ServeContent copiaría el segmento a mano")
	}
	if copiado != int64(len("contenido del segmento")) {
		t.Errorf("ReadFrom copió %d bytes, quiero %d", copiado, len("contenido del segmento"))
	}
	// El writer de httptest no implementa io.ReaderFrom, así que este camino es
	// justamente el io.Copy de respaldo: los bytes tienen que llegar igual.
	if got := w.Body.String(); got != "contenido del segmento" {
		t.Errorf("cuerpo = %q", got)
	}
}
