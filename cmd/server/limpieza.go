package main

import (
	"context"
	"log"
	"time"

	"zapping-live/internal/auth"
)

// limpiarSesiones borra periódicamente las sesiones vencidas.
//
// Le da llamante a Sessions.Limpiar, que hasta este bloque no tenía ninguno:
// sin esta goroutine la tabla `sessions` crece indefinidamente, porque cada
// login deja una fila que nadie borra cuando vence. Es una fuga lenta pero
// real, y en un servicio pensado para quedarse levantado importa.
//
// Vuelve cuando se cancela el contexto, que es lo que la ata al apagado
// ordenado del proceso en vez de dejarla corriendo hasta que el runtime muera.
func limpiarSesiones(ctx context.Context, s *auth.Sessions, cada time.Duration, registro *log.Logger) {
	// Un barrido de entrada, antes del primer tic: si el proceso estuvo caído
	// un día, arranca con la tabla llena de filas ya vencidas y esperar el
	// intervalo completo sería gratuito.
	barrer(ctx, s, registro)

	t := time.NewTicker(cada)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			barrer(ctx, s, registro)
		case <-ctx.Done():
			return
		}
	}
}

func barrer(ctx context.Context, s *auth.Sessions, registro *log.Logger) {
	n, err := s.Limpiar(ctx)
	if err != nil {
		// Un fallo acá no es motivo para tumbar el servicio: la próxima vuelta
		// lo reintenta. Pero se registra, o la tabla crecería en silencio.
		registro.Printf("limpieza de sesiones: %v", err)
		return
	}
	if n > 0 {
		registro.Printf("limpieza de sesiones: %d vencidas eliminadas", n)
	}
}
