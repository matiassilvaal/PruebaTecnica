// Command server levanta el servicio de livestreaming.
//
// Es el único lugar donde se cablean los paquetes entre sí: cada uno de ellos
// recibe sus dependencias ya construidas y no busca ninguna por su cuenta.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"zapping-live/internal/auth"
	"zapping-live/internal/config"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/hls"
	"zapping-live/internal/storage"
	"zapping-live/internal/viewers"
	"zapping-live/internal/web"
)

// intervaloLimpieza es cada cuánto se barren las sesiones vencidas. Una hora
// alcanza de sobra: la tabla no crece rápido y el barrido es un DELETE sobre
// un índice.
const intervaloLimpieza = time.Hour

// plazoApagado es lo que se le da a las conexiones en curso al recibir la
// señal. Las de SSE ya se habrán ido por su cuenta: la cancelación del
// contexto cierra el hub y sus handlers vuelven.
const plazoApagado = 10 * time.Second

// nuevoServidor arma el http.Server con sus tiempos límite.
//
// Está separado de run() para que un test pueda afirmar sobre esos tiempos:
// la ausencia de WriteTimeout es una decisión de diseño, y sin un test que la
// fije, agregarlo un día "por prudencia" cortaría todas las conexiones SSE sin
// que nada se quejara.
func nuevoServidor(puerto string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:    ":" + puerto,
		Handler: h,
		// Contra slowloris, sin tocar las respuestas largas.
		ReadHeaderTimeout: 10 * time.Second,
		// SIN WriteTimeout a propósito: cortaría toda conexión SSE a los pocos
		// segundos, que es justo lo que este servicio necesita mantener
		// abierto. El problema que WriteTimeout resuelve —clientes que no
		// leen— ya está cubierto por el backpressure del hub.
	}
}

func main() {
	registro := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	if err := run(registro); err != nil {
		registro.Fatalf("el servidor no pudo continuar: %v", err)
	}
	registro.Print("apagado limpio")
}

func run(registro *log.Logger) error {
	cfg, err := config.Cargar()
	if err != nil {
		return fmt.Errorf("configuración: %w", err)
	}

	// NotifyContext ata SIGINT y SIGTERM al contexto raíz: `docker stop` y
	// Ctrl+C recorren exactamente el mismo camino de apagado.
	ctx, detener := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer detener()

	db, err := storage.Open(ctx, cfg.RutaDB)
	if err != nil {
		return fmt.Errorf("abriendo la base en %q: %w", cfg.RutaDB, err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrando la base: %w", err)
	}

	// Si no hay segmentos, NO se levanta. Un servidor arriba sirviendo 404
	// parece sano —responde, el healthcheck pasa— y el problema aparecería
	// recién cuando alguien intente ver el stream.
	manifiesto := filepath.Join(cfg.DirSegmentos, "segment.m3u8")
	pool, err := hls.ParseManifest(manifiesto)
	if err != nil {
		return fmt.Errorf("leyendo el manifiesto %q (¿corriste scripts/prepare-segments?): %w", manifiesto, err)
	}
	registro.Printf("pool cargado: %d segmentos desde %s", pool.Len(), cfg.DirSegmentos)

	hub := viewers.NewHub()

	// El hook conecta el motor con el hub sin que hls conozca al hub ni al
	// revés. Corre SÍNCRONAMENTE en la goroutine de rotación: Hub.Publicar
	// descarta en vez de bloquear justamente por esto.
	motor := hls.New(pool,
		hls.WithWindowSize(cfg.TamVentana),
		hls.WithRotationHook(web.HookDeRotacion(hub)),
	)

	usuarios := cuenta.NewStore(db)
	sesiones := auth.NewSessions(db, cfg.TTLSesion)
	guard := auth.NewGuard(sesiones, usuarios, cfg.CookiesSeguras)

	go hub.Run(ctx)
	// Engine.Run EXACTAMENTE UNA VEZ por instancia, y este es el único
	// llamante del proyecto: dos goroutines romperían la monotonía de
	// EXT-X-MEDIA-SEQUENCE, que es lo que el enunciado pide garantizar.
	go motor.Run(ctx)
	go limpiarSesiones(ctx, sesiones, intervaloLimpieza, registro)

	srv := nuevoServidor(cfg.Puerto, web.NewRouter(web.Deps{
		Motor:    motor,
		Pool:     pool,
		Hub:      hub,
		Guard:    guard,
		Sesiones: sesiones,
		Usuarios: usuarios,
		Salud:    db.PingContext,
		Log:      registro,
	}))

	errores := make(chan error, 1)
	go func() {
		registro.Printf("escuchando en http://localhost:%s", cfg.Puerto)
		errores <- srv.ListenAndServe()
	}()

	select {
	case err := <-errores:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("escuchando: %w", err)
		}
	case <-ctx.Done():
		registro.Print("señal recibida, apagando")
	}

	// El orden importa: al llegar acá el contexto raíz ya está cancelado, así
	// que el hub cerró los canales de sus clientes y los handlers SSE ya
	// volvieron. Si Shutdown fuera primero, esperaría el plazo completo a que
	// se cerraran unas conexiones diseñadas para no cerrarse nunca.
	ctxApagado, cancelar := context.WithTimeout(context.Background(), plazoApagado)
	defer cancelar()
	if err := srv.Shutdown(ctxApagado); err != nil {
		return fmt.Errorf("apagando el servidor: %w", err)
	}
	return nil
}
