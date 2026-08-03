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

func contarSesiones(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("contando sesiones: %v", err)
	}
	return n
}
