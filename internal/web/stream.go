package web

import (
	"log"
	"net/http"
	"os"
	"time"

	"zapping-live/internal/hls"
	"zapping-live/internal/viewers"
)

// manejadorStream sirve el playlist y los segmentos.
type manejadorStream struct {
	motor *hls.Engine
	pool  *hls.Pool
	log   *log.Logger
}

// playlist entrega el .m3u8 vigente.
//
// Todo el trabajo ya está hecho: el snapshot trae el playlist renderizado desde
// la rotación, así que esta petición es un Load atómico y un Write de bytes
// listos. Cero formateo y cero asignaciones por espectador.
func (s *manejadorStream) playlist(w http.ResponseWriter, r *http.Request) {
	snap := s.motor.Current() // wait-free: no toma locks ni compite con el motor

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Obligatorio: un playlist cacheado le entrega al player una ventana vieja,
	// que lo lleva a pedir segmentos ya expirados y a cortar la reproducción.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// snap.Playlist es de SÓLO LECTURA y compartido por referencia entre todos
	// los lectores: se escribe tal cual, nunca se modifica.
	w.Write(snap.Playlist)
}

// segmento entrega un .ts.
//
// El nombre se valida contra el pool, que es una lista blanca construida al
// parsear el manifiesto: un nombre que no está ahí no resuelve, y con eso el
// path traversal queda cerrado sin tener que sanear cadenas. Hace falta de
// verdad, porque el mux des-escapa el comodín: "%2e%2e%2f" llega acá como
// "../" sin pasar por la limpieza de ruta de net/http.
func (s *manejadorStream) segmento(w http.ResponseWriter, r *http.Request) {
	nombre := r.PathValue("name")
	ruta, ok := s.pool.Resolve(nombre)
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(ruta)
	if err != nil {
		// Un segmento que falta es un problema de despliegue, no del cliente:
		// se registra, pero no se tumba el stream por eso.
		s.log.Printf("web: abriendo el segmento %q: %v", nombre, err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.log.Printf("web: stat del segmento %q: %v", nombre, err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}

	// El contenido de un .ts nunca cambia, así que se cachea un año. Es lo que
	// hace que revisitar la ventana no vuelva a costarle disco al servidor.
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	// ServeContent copia por bloques desde el *os.File y resuelve los range
	// requests. Un segmento de 13 MB cuesta del orden de KB de RAM, no 13 MB:
	// es una de las tres decisiones de memoria del proyecto.
	http.ServeContent(w, r, nombre, info.ModTime(), f)
}

// HookDeRotacion adapta un snapshot del motor a un evento del hub.
//
// Vive en `web` porque es el único punto que necesita conocer a la vez `hls` y
// `viewers`: así el hub no importa `hls` y `hls` sigue sin saber que existe un
// hub.
//
// CORRE SÍNCRONAMENTE EN LA GOROUTINE DE ROTACIÓN DEL MOTOR. No puede bloquear
// ni entrar en pánico: lo primero detendría el avance del stream para todos los
// espectadores, lo segundo tumbaría la goroutine del motor. Hub.Publicar
// descarta en vez de bloquear, y acá no hay nada que pueda entrar en pánico.
func HookDeRotacion(h *viewers.Hub) func(*hls.Snapshot) {
	return func(s *hls.Snapshot) {
		// Se COPIAN los nombres: s.Window es de sólo lectura y compartido por
		// referencia con todos los lectores concurrentes. Es una asignación
		// cada ~10 segundos, no por petición.
		ventana := make([]string, len(s.Window))
		for i, seg := range s.Window {
			ventana[i] = seg.Name
		}

		h.Publicar(viewers.Evento{
			Secuencia:      s.Seq,
			Ventana:        ventana,
			ProximaEnMs:    time.Until(s.NextAt).Milliseconds(),
			Discontinuidad: s.HasDisc,
		})
	}
}
