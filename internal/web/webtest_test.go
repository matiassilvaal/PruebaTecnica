package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/hls"
	"zapping-live/internal/storage/storagetest"
	"zapping-live/internal/viewers"
)

// banco es el entorno completo de un test: el router listo para usar más las
// piezas sueltas, por si el test necesita hablarles directamente.
type banco struct {
	Handler  http.Handler
	Motor    *hls.Engine
	Pool     *hls.Pool
	Hub      *viewers.Hub
	Guard    *auth.Guard
	Sesiones *auth.Sessions
	Usuarios *cuenta.Store

	// Registro acumula lo que el middleware de logging escribiría a stderr.
	// Mandarlo a un buffer en vez de os.Stderr evita que la línea por petición
	// entierre la salida de `go test`; los tests que necesiten inspeccionar el
	// log lo leen de acá.
	Registro *bufferDeLog

	// Cancelar apaga el hub sin esperar a que termine el test. Lo necesitan
	// los tests que verifican qué pasa con un espectador todavía conectado
	// cuando el proceso se apaga (el camino real de `docker stop`).
	Cancelar context.CancelFunc
}

// hashBarato reemplaza a auth.HashPassword en los tests.
//
// auth.HashPassword usa costo 12 (~370 ms por llamada, a propósito). Pagarlo
// por cada usuario de prueba haría que la suite tardara minutos. bcrypt.MinCost
// produce un hash igual de válido para VerifyPassword, que no mira el costo.
func hashBarato(plano string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plano), bcrypt.MinCost)
	return string(b), err
}

// entorno arma base migrada, pool de segmentos falsos, motor, hub y router.
//
// El hub corre de verdad: los tests del SSE lo necesitan vivo y a los demás no
// les molesta. Se apaga con t.Cleanup.
func entorno(t *testing.T) *banco {
	t.Helper()

	db := storagetest.AbrirMigrada(t)
	usuarios := cuenta.NewStore(db)
	sesiones := auth.NewSessions(db, time.Hour)
	guard := auth.NewGuard(sesiones, usuarios, false)

	pool := poolDePrueba(t)
	// Sin WithRotationHook y sin Run: Current() ya devuelve un snapshot válido
	// desde New, y arrancar el motor haría los tests dependientes del reloj.
	motor := hls.New(pool, hls.WithWindowSize(3))

	hub := viewers.NewHub()
	ctx, cancelar := context.WithCancel(context.Background())
	go hub.Run(ctx)
	t.Cleanup(cancelar)

	registro := &bufferDeLog{}
	b := &banco{
		Motor: motor, Pool: pool, Hub: hub,
		Guard: guard, Sesiones: sesiones, Usuarios: usuarios,
		Registro: registro,
		Cancelar: cancelar,
	}
	b.Handler = NewRouter(Deps{
		Motor: motor, Pool: pool, Hub: hub,
		Guard: guard, Sesiones: sesiones, Usuarios: usuarios,
		Salud: func(context.Context) error { return nil },
		Log:   log.New(registro, "test: ", 0),
	})
	return b
}

// poolDePrueba escribe un manifiesto y cuatro .ts diminutos en un directorio
// temporal.
//
// El último dura 4.566667 s a propósito: es la duración real de segment63.ts en
// el material provisto, y el caso que descarta la solución del ticker fijo.
func poolDePrueba(t *testing.T) *hls.Pool {
	t.Helper()
	dir := t.TempDir()

	duraciones := []float64{10, 10, 10, 4.566667}
	var m strings.Builder
	m.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for i, d := range duraciones {
		nombre := fmt.Sprintf("segment%d.ts", i)
		fmt.Fprintf(&m, "#EXTINF:%.6f,\n%s\n", d, nombre)
		// Contenido reconocible: los tests verifican qué bytes salieron.
		cuerpo := []byte("contenido-falso-del-" + nombre)
		if err := os.WriteFile(filepath.Join(dir, nombre), cuerpo, 0o644); err != nil {
			t.Fatalf("escribiendo %s: %v", nombre, err)
		}
	}
	m.WriteString("#EXT-X-ENDLIST\n")

	ruta := filepath.Join(dir, "segment.m3u8")
	if err := os.WriteFile(ruta, []byte(m.String()), 0o644); err != nil {
		t.Fatalf("escribiendo el manifiesto: %v", err)
	}
	p, err := hls.ParseManifest(ruta)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return p
}

// usuarioConSesion da de alta un usuario y devuelve su cookie de sesión.
//
// Usa cuenta.Registrar —no Store.Crear— porque es la única vía que valida y
// hashea, igual que hace el handler de registro.
func usuarioConSesion(t *testing.T, b *banco) (*cuenta.Usuario, *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	u, err := cuenta.Registrar(ctx, b.Usuarios, hashBarato, "Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	token, err := b.Sesiones.Crear(ctx, u.ID)
	if err != nil {
		t.Fatalf("Sesiones.Crear: %v", err)
	}
	return u, &http.Cookie{Name: auth.NombreCookie, Value: token}
}

// bufferDeLog acumula lo que escriba un *log.Logger.
type bufferDeLog struct{ b strings.Builder }

func (l *bufferDeLog) Write(p []byte) (int, error) { return l.b.Write(p) }
func (l *bufferDeLog) String() string              { return l.b.String() }
