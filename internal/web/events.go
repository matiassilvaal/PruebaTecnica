package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"zapping-live/internal/viewers"
)

// intervaloLatido mantiene viva la conexión entre rotaciones.
//
// Pueden pasar 10 segundos sin un solo byte, y un proxy inverso con timeout de
// inactividad corta ahí. El comentario SSE que se manda cada 20 s no llega a
// EventSource —el navegador descarta los comentarios— pero sí cuenta como
// tráfico para el proxy.
const intervaloLatido = 20 * time.Second

type manejadorEventos struct {
	hub *viewers.Hub
	log *log.Logger
}

// sse mantiene abierta una conexión de eventos hacia un espectador.
func (m *manejadorEventos) sse(w http.ResponseWriter, r *http.Request) {
	vaciar, ok := w.(http.Flusher)
	if !ok {
		// Inalcanzable con net/http, pero un envoltorio mal escrito lo haría
		// posible: sin Flush los eventos se quedarían en el buffer y el panel
		// no se movería nunca. Mejor fallar fuerte que quedarse mudo.
		m.log.Print("web: el ResponseWriter no implementa http.Flusher; el SSE no puede funcionar")
		http.Error(w, "streaming no disponible", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx bufferiza las respuestas por defecto y eso convierte un stream de
	// eventos en una entrega a bloques: el panel se movería a saltos o no se
	// movería nunca.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Manda las cabeceras ya. Hoy el hub siempre deja un evento listo antes de
	// que Suscribir vuelva, así que no cambia nada observable; existe para que
	// el tiempo hasta el primer byte no dependa de cuánto tarde Suscribir, en
	// vez de heredar en silencio una garantía que vive en otro paquete.
	vaciar.Flush()

	// Suscribir devuelve un canal ya con el estado vigente adentro, así que el
	// panel se pinta al instante en vez de esperar a la próxima rotación.
	eventos, salir := m.hub.Suscribir()
	defer salir()

	latido := time.NewTicker(intervaloLatido)
	defer latido.Stop()

	for {
		select {
		case e, abierto := <-eventos:
			if !abierto {
				return // el hub se apagó: se cierra la conexión ordenadamente
			}
			cuerpo, err := json.Marshal(e)
			if err != nil {
				// viewers.Evento es JSON-serializable por construcción; si
				// esto ocurriera, sería un cambio de tipo mal hecho.
				m.log.Printf("web: serializando el evento: %v", err)
				continue
			}
			// El formato de SSE: "data: <carga>" y una línea en blanco que
			// cierra el mensaje. Sin esa línea, el navegador espera más y no
			// entrega nada.
			if _, err := w.Write(append(append([]byte("data: "), cuerpo...), '\n', '\n')); err != nil {
				return // el espectador se fue a mitad de escritura
			}
			vaciar.Flush()

		case <-latido.C:
			if _, err := w.Write([]byte(": latido\n\n")); err != nil {
				return
			}
			vaciar.Flush()

		case <-r.Context().Done():
			// El espectador cerró la pestaña. Esto es lo que garantiza que no
			// queden goroutines huérfanas: el defer de arriba lo da de baja.
			return
		}
	}
}
