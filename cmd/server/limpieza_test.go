package main

import (
	"context"
	"database/sql"
	"io"
	"log"
	"testing"
	"time"

	"zapping-live/internal/auth"
	"zapping-live/internal/cuenta"
	"zapping-live/internal/storage/storagetest"
)

func TestLimpiarSesionesBorraLasVencidas(t *testing.T) {
	// Sin esta goroutine, la tabla sessions crece indefinidamente: cada login
	// deja una fila que nadie borra al vencer. Fuga lenta, pero real.
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()

	usuarios := cuenta.NewStore(db)
	u, err := cuenta.Registrar(ctx, usuarios, func(p string) (string, error) { return "hash-" + p, nil },
		"Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	// Una sesión emitida "ayer" con TTL de una hora ya está vencida.
	ayer := time.Now().Add(-24 * time.Hour)
	viejas := auth.NewSessions(db, time.Hour, auth.ConReloj(func() time.Time { return ayer }))
	if _, err := viejas.Crear(ctx, u.ID); err != nil {
		t.Fatalf("creando la sesión vencida: %v", err)
	}

	sesiones := auth.NewSessions(db, time.Hour)
	if n := contarSesiones(t, db); n != 1 {
		t.Fatalf("sesiones antes = %d, quiero 1", n)
	}

	ctxLimpieza, cancelar := context.WithCancel(ctx)
	defer cancelar()
	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		limpiarSesiones(ctxLimpieza, sesiones, 10*time.Millisecond, log.New(io.Discard, "", 0))
	}()

	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		if contarSesiones(t, db) == 0 {
			cancelar()
			select {
			case <-hecho:
				return // también comprueba que la goroutine termina al cancelar
			case <-time.After(time.Second):
				t.Fatal("limpiarSesiones no volvió tras cancelar el contexto")
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("la sesión vencida sigue en la base tras 2 s")
}

func TestLimpiarSesionesRespetaLasVigentes(t *testing.T) {
	// Un barrido que se llevara las sesiones vivas desconectaría a todo el
	// mundo cada pocos minutos.
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()

	usuarios := cuenta.NewStore(db)
	u, err := cuenta.Registrar(ctx, usuarios, func(p string) (string, error) { return "hash-" + p, nil },
		"Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	sesiones := auth.NewSessions(db, time.Hour)
	if _, err := sesiones.Crear(ctx, u.ID); err != nil {
		t.Fatalf("Crear: %v", err)
	}

	ctxLimpieza, cancelar := context.WithCancel(ctx)
	go limpiarSesiones(ctxLimpieza, sesiones, 5*time.Millisecond, log.New(io.Discard, "", 0))
	time.Sleep(100 * time.Millisecond)
	cancelar()

	if n := contarSesiones(t, db); n != 1 {
		t.Fatalf("sesiones vigentes = %d, quiero 1: la limpieza se llevó una sesión viva", n)
	}
}

func TestLimpiarSesionesBarreDeFormaPeriodica(t *testing.T) {
	// El test de arriba no distingue "limpia una vez al arrancar" de "limpia
	// siempre": el barrido inicial ya se lleva la primera sesión vencida. Acá
	// la segunda se crea DESPUÉS de que ese primer barrido ocurrió, así que
	// sólo un barrido periódico puede eliminarla. Sin esto, cambiar el bucle
	// por una sola pasada dejaba la suite en verde y la tabla creciendo.
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()

	usuarios := cuenta.NewStore(db)
	u, err := cuenta.Registrar(ctx, usuarios, func(p string) (string, error) { return "hash-" + p, nil },
		"Ana Prueba", "ana@ejemplo.cl", "contrasena-larga")
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	ayer := time.Now().Add(-24 * time.Hour)
	viejas := auth.NewSessions(db, time.Hour, auth.ConReloj(func() time.Time { return ayer }))
	sesiones := auth.NewSessions(db, time.Hour)

	crearVencida := func() {
		t.Helper()
		if _, err := viejas.Crear(ctx, u.ID); err != nil {
			t.Fatalf("creando la sesión vencida: %v", err)
		}
	}
	esperarVacio := func(motivo string) {
		t.Helper()
		limite := time.Now().Add(time.Second)
		for time.Now().Before(limite) {
			if contarSesiones(t, db) == 0 {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("se agotó el plazo: %s", motivo)
	}

	crearVencida()
	ctxLimpieza, cancelar := context.WithCancel(ctx)
	defer cancelar()
	go limpiarSesiones(ctxLimpieza, sesiones, 10*time.Millisecond, log.New(io.Discard, "", 0))

	esperarVacio("el primer barrido no borró la sesión vencida")

	// La segunda llega con la goroutine ya corriendo: sólo un barrido que se
	// repita puede alcanzarla.
	crearVencida()
	esperarVacio("no hubo un segundo barrido: la limpieza no es periódica")
}

func TestElServidorNoLlevaWriteTimeout(t *testing.T) {
	// Un WriteTimeout cortaría TODA conexión SSE a los pocos segundos, que es
	// justo lo que este servicio necesita mantener abierta. El problema que
	// resuelve —clientes que no leen— ya lo cubre el backpressure del hub, y
	// contra slowloris está ReadHeaderTimeout, que no toca las respuestas
	// largas. Sin este test, agregarlo un día "por prudencia" rompería el
	// panel en vivo sin que nada se quejara.
	srv := nuevoServidor("8080", nil)

	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, quiero 0: cortaría las conexiones SSE", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout = 0: hace falta contra slowloris")
	}
	// IdleTimeout va junto en la misma aserción a propósito: es el que sí
	// corresponde poner —sólo corre ENTRE peticiones, nunca durante un SSE en
	// curso— y tenerlo acá deja claro que la ausencia de WriteTimeout es una
	// decisión y no un olvido, para que nadie los "arregle" a los dos juntos.
	if srv.IdleTimeout == 0 {
		t.Error("IdleTimeout = 0: las conexiones keep-alive ociosas quedarían retenidas sin límite")
	}
	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q, quiero \":8080\"", srv.Addr)
	}
}

func contarSesiones(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("contando sesiones: %v", err)
	}
	return n
}
