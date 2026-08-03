package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/hls"
	"zapping-live/internal/viewers"
)

func TestPlaylistCabeceras(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("Content-Type = %q, quiero application/vnd.apple.mpegurl", ct)
	}
	// Sin no-store, un proxy entrega una ventana vieja, el player pide
	// segmentos que ya salieron de la ventana y la reproducción se corta.
	cc := w.Header().Get("Cache-Control")
	for _, quiero := range []string{"no-cache", "no-store"} {
		if !strings.Contains(cc, quiero) {
			t.Errorf("Cache-Control = %q, le falta %q", cc, quiero)
		}
	}
}

func TestPlaylistEsElDelSnapshot(t *testing.T) {
	// Lo que este test fija es el CONTENIDO: el cuerpo es byte a byte el
	// playlist del snapshot vigente, y sus URI son relativas a segments/ —
	// montar el .m3u8 fuera de la ruta hermana de los segmentos rompe esos
	// enlaces.
	//
	// Lo que NO fija, ni acá ni en TestPlaylistNoMutaElSnapshotCompartido: que
	// el playlist no se re-renderice por request. Un re-render determinista
	// produce exactamente los mismos bytes y no toca el array compartido, así
	// que ninguno de los dos lo vería. Detectarlo exigiría contar asignaciones
	// (testing.AllocsPerRun sobre el handler), y no se hizo.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	quiero := b.Motor.Current().Playlist
	if !bytes.Equal(w.Body.Bytes(), quiero) {
		t.Fatalf("cuerpo = %q, quiero %q", w.Body.String(), quiero)
	}
	if !strings.Contains(w.Body.String(), "segments/segment0.ts") {
		t.Errorf("las URI deben ser relativas a segments/, el cuerpo es:\n%s", w.Body.String())
	}
}

func TestPlaylistNoMutaElSnapshotCompartido(t *testing.T) {
	// Snapshot.Playlist es de SÓLO LECTURA: lo comparten por referencia todos
	// los lectores concurrentes. Dos peticiones seguidas tienen que ver los
	// mismos bytes; si el handler escribiera sobre ellos, la segunda vería
	// basura.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)
	antes := append([]byte(nil), b.Motor.Current().Playlist...)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/live/stream.m3u8", nil)
		r.AddCookie(galleta)
		b.Handler.ServeHTTP(httptest.NewRecorder(), r)
	}

	if !bytes.Equal(antes, b.Motor.Current().Playlist) {
		t.Fatal("el handler mutó Snapshot.Playlist, que es compartido y de sólo lectura")
	}
}

func TestPlaylistSinSesionEs401(t *testing.T) {
	// 401 y NO 302: con un redirect, hls.js intentaría parsear la página de
	// login como playlist y reportaría un error incomprensible.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/live/stream.m3u8", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", w.Code)
	}
}

func TestSegmentoSirveElArchivo(t *testing.T) {
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/segments/segment1.ts", nil)
	r.AddCookie(galleta)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("código = %d, quiero 200", w.Code)
	}
	if got := w.Body.String(); got != "contenido-falso-del-segment1.ts" {
		t.Errorf("cuerpo = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("Content-Type = %q, quiero video/mp2t", ct)
	}
	// Los .ts no cambian nunca: cachearlos agresivamente le ahorra al servidor
	// toda la ventana ya vista.
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control = %q, quiero private, max-age=31536000, immutable", cc)
	}
	// Y `private`, nunca `public`: la ruta va detrás de RequireAPI, y una
	// cookie de sesión no impide por sí sola que una caché compartida guarde la
	// respuesta. Con `public` cualquier proxy quedaría autorizado a servir un
	// segmento autenticado durante un año a quien no tiene cuenta, que es
	// exactamente lo que el requisito 4 prohíbe.
	if !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q, le falta private", cc)
	}
	if strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q: no puede ser public detrás de una sesión", cc)
	}
}

func TestSegmentoSoportaRangos(t *testing.T) {
	// http.ServeContent da range requests sin código extra, y es lo que permite
	// que el player pida trozos en vez del archivo entero. También es la razón
	// de servir con ServeContent en vez de io.Copy.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	r := httptest.NewRequest("GET", "/live/segments/segment0.ts", nil)
	r.AddCookie(galleta)
	r.Header.Set("Range", "bytes=0-8")
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, r)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("código = %d, quiero 206", w.Code)
	}
	if got := w.Body.String(); got != "contenido" {
		t.Errorf("cuerpo = %q, quiero \"contenido\"", got)
	}
}

func TestSegmentoTraversal(t *testing.T) {
	// Pool.Resolve es una lista blanca contra el índice del manifiesto: un
	// nombre que no está en el pool no resuelve, punto. Las variantes
	// codificadas existen porque el mux de Go des-escapa el comodín, así que
	// %2e%2e%2f llega al handler como "../" sin pasar por la limpieza de ruta.
	b := entorno(t)
	_, galleta := usuarioConSesion(t, b)

	rutas := []string{
		"/live/segments/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/live/segments/%2e%2e%2fsegment.m3u8",
		"/live/segments/..%2fsegment.m3u8",
		"/live/segments/segment.m3u8",
		"/live/segments/segment99.ts",
	}
	for _, ruta := range rutas {
		t.Run(ruta, func(t *testing.T) {
			r := httptest.NewRequest("GET", ruta, nil)
			r.AddCookie(galleta)
			w := httptest.NewRecorder()
			b.Handler.ServeHTTP(w, r)

			if w.Code == http.StatusOK {
				t.Fatalf("código = 200: sirvió algo que no está en el pool, cuerpo = %q", w.Body.String())
			}
			if w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
				t.Errorf("código = %d, quiero 404 (o 301 si el mux limpió la ruta)", w.Code)
			}
		})
	}
}

func TestSegmentoSinSesionEs401(t *testing.T) {
	// Proteger sólo /player dejaría los .ts descargables sin cuenta, que es
	// exactamente lo que el requisito 4 busca impedir.
	b := entorno(t)
	w := httptest.NewRecorder()
	b.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/live/segments/segment0.ts", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("código = %d, quiero 401", w.Code)
	}
}

func TestHookDeRotacionArmaElEvento(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := viewers.NewHub()
	go h.Run(ctx)

	ch, salir := h.Suscribir()
	defer salir()
	for len(ch) > 0 {
		<-ch
	}

	snap := &hls.Snapshot{
		Seq:     142,
		HasDisc: true,
		Window: []hls.Segment{
			{Name: "segment14.ts", Duration: 10 * time.Second},
			{Name: "segment15.ts", Duration: 10 * time.Second},
			{Name: "segment16.ts", Duration: 10 * time.Second},
		},
		NextAt: time.Now().Add(4 * time.Second),
	}
	HookDeRotacion(h)(snap)

	select {
	case e := <-ch:
		if e.Secuencia != 142 {
			t.Errorf("Secuencia = %d, quiero 142", e.Secuencia)
		}
		if !e.Discontinuidad {
			t.Error("Discontinuidad = false, quiero true")
		}
		quiero := []string{"segment14.ts", "segment15.ts", "segment16.ts"}
		if len(e.Ventana) != len(quiero) {
			t.Fatalf("Ventana = %v, quiero %v", e.Ventana, quiero)
		}
		for i := range quiero {
			if e.Ventana[i] != quiero[i] {
				t.Errorf("Ventana[%d] = %q, quiero %q", i, e.Ventana[i], quiero[i])
			}
		}
		if e.ProximaEnMs <= 0 || e.ProximaEnMs > 4000 {
			t.Errorf("ProximaEnMs = %d, quiero algo en (0, 4000]", e.ProximaEnMs)
		}
	case <-time.After(time.Second):
		t.Fatal("el hub no recibió el evento del hook")
	}
}

func TestHookDeRotacionNoBloqueaConElHubDetenido(t *testing.T) {
	// LA restricción heredada del bloque 02: el hook corre síncronamente en la
	// goroutine que hace avanzar el stream. Si se bloqueara —por ejemplo
	// porque el hub todavía no arrancó o ya se apagó— el stream se detendría
	// para todos los espectadores. Sin el select/default de Publicar, este
	// test cuelga.
	h := viewers.NewHub() // Run nunca se llama
	hook := HookDeRotacion(h)

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := 0; i < 200; i++ {
			hook(&hls.Snapshot{Seq: int64(i), NextAt: time.Now().Add(time.Second)})
		}
	}()
	select {
	case <-hecho:
	case <-time.After(2 * time.Second):
		t.Fatal("el hook se bloqueó: eso detendría el avance del stream")
	}
}

func TestHookDeRotacionNoMutaElSnapshot(t *testing.T) {
	// Snapshot.Window es de sólo lectura y se comparte entre todos los
	// lectores. El hook tiene que COPIAR los nombres, no quedarse con el slice.
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	h := viewers.NewHub()
	go h.Run(ctx)

	ventana := []hls.Segment{{Name: "segment0.ts", Duration: 10 * time.Second}}
	snap := &hls.Snapshot{Seq: 1, Window: ventana, NextAt: time.Now().Add(time.Second)}
	HookDeRotacion(h)(snap)

	if snap.Window[0].Name != "segment0.ts" || len(snap.Window) != 1 {
		t.Fatalf("el hook modificó Snapshot.Window: %+v", snap.Window)
	}
}
