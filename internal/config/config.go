// Package config lee la configuración del entorno.
//
// Todo tiene default: el servicio levanta sin configurar nada, que es lo que
// hace que `docker run` funcione con un solo comando.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config es la configuración efectiva del servidor.
type Config struct {
	Puerto         string        // PORT
	RutaDB         string        // DB_PATH
	DirSegmentos   string        // SEGMENTS_DIR
	TTLSesion      time.Duration // SESSION_TTL
	CookiesSeguras bool          // SECURE_COOKIES
	TamVentana     int           // WINDOW_SIZE
}

// Cargar arma la configuración desde el entorno.
//
// Una variable AUSENTE cae al default; una variable PRESENTE pero ilegible es
// un error. La distinción importa: caer al default ante un valor mal escrito
// dejaría al operador convencido de haber configurado algo que nunca se
// aplicó, y el síntoma aparecería mucho después y en otro lado.
func Cargar() (Config, error) {
	c := Config{
		Puerto:       "8080",
		RutaDB:       "/data/zapping.db",
		DirSegmentos: "/app/segments",
		TTLSesion:    24 * time.Hour,
		// Default false: sin HTTPS por delante, una cookie Secure no viaja y
		// nadie podría iniciar sesión. En producción va detrás de un proxy TLS
		// y esto pasa a true.
		CookiesSeguras: false,
		// 3 lo fija el enunciado: "3 segmentos (30 segundos) por request".
		TamVentana: 3,
	}

	if v, ok := os.LookupEnv("PORT"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("PORT está definido pero vacío")
		}
		c.Puerto = v
	}
	if v, ok := os.LookupEnv("DB_PATH"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("DB_PATH está definido pero vacío")
		}
		c.RutaDB = v
	}
	if v, ok := os.LookupEnv("SEGMENTS_DIR"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("SEGMENTS_DIR está definido pero vacío")
		}
		c.DirSegmentos = v
	}
	if v, ok := os.LookupEnv("SESSION_TTL"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("SESSION_TTL=%q no es una duración válida (ejemplos: 24h, 45m): %w", v, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("SESSION_TTL=%q debe ser positivo", v)
		}
		c.TTLSesion = d
	}
	if v, ok := os.LookupEnv("SECURE_COOKIES"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SECURE_COOKIES=%q no es booleano (true/false): %w", v, err)
		}
		c.CookiesSeguras = b
	}
	if v, ok := os.LookupEnv("WINDOW_SIZE"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("WINDOW_SIZE=%q no es un entero: %w", v, err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("WINDOW_SIZE=%q debe ser positivo", v)
		}
		c.TamVentana = n
	}

	return c, nil
}
