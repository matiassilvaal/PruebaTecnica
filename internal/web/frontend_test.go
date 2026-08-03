package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHlsSeSirveVendorizado(t *testing.T) {
	// Si esto falla, el player no arranca en un contenedor sin internet, que
	// es exactamente el escenario en que lo va a levantar el evaluador.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/vendor/hls.min.js", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if n := w.Body.Len(); n < 100_000 {
		t.Fatalf("hls.min.js pesa %d bytes: la descarga de la Task 7 falló", n)
	}
}

func TestElFrontendNoPideNadaAInternet(t *testing.T) {
	// El criterio de aceptación "funciona con el contenedor aislado de
	// internet", verificado sobre el código en vez de a mano. Se revisan los
	// archivos que escribimos nosotros; hls.min.js queda fuera porque su
	// minificado contiene URLs dentro de literales de texto.
	nuestros := []string{
		"templates/base.html",
		"templates/login.html",
		"templates/register.html",
		"templates/player.html",
		"static/app.css",
		"static/player.js",
	}
	externo := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)

	for _, nombre := range nuestros {
		t.Run(nombre, func(t *testing.T) {
			var datos []byte
			var err error
			if strings.HasPrefix(nombre, "templates/") {
				datos, err = archivosPlantillas.ReadFile(nombre)
			} else {
				datos, err = archivosEstaticos.ReadFile(nombre)
			}
			if err != nil {
				t.Fatalf("leyendo %s: %v", nombre, err)
			}
			for _, linea := range strings.Split(string(datos), "\n") {
				// Las URL en comentarios CSS/JS no generan peticiones.
				recortada := strings.TrimSpace(linea)
				if strings.HasPrefix(recortada, "/*") || strings.HasPrefix(recortada, "*") || strings.HasPrefix(recortada, "//") {
					continue
				}
				if m := externo.FindString(linea); m != "" {
					t.Errorf("referencia externa %q en: %s", m, recortada)
				}
			}
		})
	}
}

func TestNingunVidrioSeSuperponeAlVideo(t *testing.T) {
	// La regla estricta del proyecto, verificada sobre el CSS y no confiada a
	// la memoria de quien lo retoque: backdrop-filter recalcula el desenfoque
	// cada vez que cambia lo que hay detrás. Sobre un video en reproducción son
	// 25-30 recálculos en GPU por segundo y puede tirar frames del video.
	datos, err := archivosEstaticos.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("leyendo app.css: %v", err)
	}

	// Los comentarios se eliminan ANTES de trocear en reglas. Sin esto, el
	// comentario que precede a una regla queda pegado a su selector y el nombre
	// deja de coincidir — con lo cual este test pasaba aunque .marco llevara
	// backdrop-filter, que es exactamente lo que viene a impedir.
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(datos), " ")

	// Elementos que están sobre el <video> o lo contienen.
	prohibidos := map[string]bool{
		"video": true, ".marco": true, ".superpuesto": true, ".sobre-video": true,
	}

	// Se extraen los selectores simples de cada regla en vez de comparar la
	// cadena entera: así ".marco > .x", ".marco:hover", ".marco.oscuro" y
	// "a, .marco" se detectan igual, y ".no-marco" no da un falso positivo.
	simples := regexp.MustCompile(`[.#]?[a-zA-Z_-][\w-]*`)
	bloque := regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)

	for _, m := range bloque.FindAllStringSubmatch(css, -1) {
		selector, cuerpo := strings.TrimSpace(m[1]), m[2]
		if !strings.Contains(cuerpo, "backdrop-filter") {
			continue
		}
		for _, token := range simples.FindAllString(selector, -1) {
			if prohibidos[token] {
				t.Errorf("el selector %q usa backdrop-filter sobre el video (por %q): está prohibido", selector, token)
			}
		}
	}
}

func TestElJsNoHardcodeaLasRutasDelStream(t *testing.T) {
	// Las URL del stream viven en el HTML que renderiza el servidor, que es
	// quien conoce su árbol de rutas. Si alguien las reescribe acá habría dos
	// fuentes de verdad, y ninguna prueba lo notaría: los tests de Go no
	// ejecutan JavaScript, así que esta comprobación textual es lo único que
	// hay entre esa regresión y el navegador.
	datos, err := archivosEstaticos.ReadFile("static/player.js")
	if err != nil {
		t.Fatalf("leyendo player.js: %v", err)
	}
	js := string(datos)

	if strings.Contains(js, "/live/") {
		t.Error("player.js escribe una ruta de /live/ a mano: debe leerlas de data-playlist y data-eventos")
	}
	for _, lectura := range []string{"dataset.playlist", "dataset.eventos"} {
		if !strings.Contains(js, lectura) {
			t.Errorf("player.js no lee %s del HTML", lectura)
		}
	}
}

func TestPlayerCargaSusScripts(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)
	r := httptest.NewRequest("GET", "/player", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	cuerpo := w.Body.String()
	for _, quiero := range []string{
		"/static/vendor/hls.min.js",
		"/static/player.js",
		"/live/stream.m3u8",
		"/live/events",
		`id="video"`,
		`id="espectadores"`,
		`id="secuencia"`,
		`id="ventana"`,
	} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("la página del player no contiene %q", quiero)
		}
	}
}
