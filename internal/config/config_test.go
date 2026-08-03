package config

import (
	"os"
	"testing"
	"time"
)

// sinEntorno deja el proceso sin ninguna de las variables que lee Cargar, y las
// restaura al terminar el test.
//
// Sin esto, el resultado lo decide el shell que corre los tests: la imagen del
// contenedor define PORT y DB_PATH, así que una etapa de test dentro del build
// haría fallar los defaults y el síntoma parecería un bug de config y no una
// contaminación del entorno.
//
// El par Setenv+Unsetenv no es redundante: Unsetenv es lo que produce la
// ausencia real, y t.Setenv es lo que registra la restauración al terminar (y,
// de paso, prohíbe t.Parallel en este test, que es la otra vía de contaminarlo).
func sinEntorno(t *testing.T) {
	t.Helper()
	for _, v := range []string{"PORT", "DB_PATH", "SEGMENTS_DIR", "SESSION_TTL", "SECURE_COOKIES", "WINDOW_SIZE"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("borrando %s del entorno: %v", v, err)
		}
	}
}

func TestCargarDefaults(t *testing.T) {
	// Sin ninguna variable puesta, el servicio debe poder levantar igual.
	sinEntorno(t)

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar sin entorno: %v", err)
	}
	if c.Puerto != "8080" {
		t.Errorf("Puerto = %q, quiero \"8080\"", c.Puerto)
	}
	if c.RutaDB != "/data/zapping.db" {
		t.Errorf("RutaDB = %q, quiero \"/data/zapping.db\"", c.RutaDB)
	}
	if c.DirSegmentos != "/app/segments" {
		t.Errorf("DirSegmentos = %q, quiero \"/app/segments\"", c.DirSegmentos)
	}
	if c.TTLSesion != 24*time.Hour {
		t.Errorf("TTLSesion = %v, quiero 24h", c.TTLSesion)
	}
	if c.CookiesSeguras {
		t.Error("CookiesSeguras = true, quiero false: el default es sin HTTPS")
	}
	if c.TamVentana != 3 {
		t.Errorf("TamVentana = %d, quiero 3: el enunciado lo fija", c.TamVentana)
	}
}

func TestCargarLeeElEntorno(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("SEGMENTS_DIR", "/tmp/segs")
	t.Setenv("SESSION_TTL", "45m")
	t.Setenv("SECURE_COOKIES", "true")
	t.Setenv("WINDOW_SIZE", "5")

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar: %v", err)
	}
	if c.Puerto != "9090" || c.RutaDB != "/tmp/x.db" || c.DirSegmentos != "/tmp/segs" {
		t.Errorf("cadenas mal leídas: %+v", c)
	}
	if c.TTLSesion != 45*time.Minute {
		t.Errorf("TTLSesion = %v, quiero 45m", c.TTLSesion)
	}
	if !c.CookiesSeguras {
		t.Error("CookiesSeguras = false, quiero true")
	}
	if c.TamVentana != 5 {
		t.Errorf("TamVentana = %d, quiero 5", c.TamVentana)
	}
}

func TestCargarRechazaValoresIlegibles(t *testing.T) {
	// Un valor presente pero ilegible es un error de operación: preferimos no
	// levantar antes que correr con una configuración que el operador cree
	// haber puesto y en realidad no se aplicó.
	casos := []struct{ variable, valor string }{
		{"SESSION_TTL", "media hora"},
		{"SESSION_TTL", "0"},
		{"SESSION_TTL", "-5m"},
		{"SECURE_COOKIES", "quizás"},
		{"WINDOW_SIZE", "tres"},
		{"WINDOW_SIZE", "0"},
		{"PORT", ""},
	}
	for _, c := range casos {
		t.Run(c.variable+"="+c.valor, func(t *testing.T) {
			t.Setenv(c.variable, c.valor)
			if _, err := Cargar(); err == nil {
				t.Fatalf("quiero error con %s=%q", c.variable, c.valor)
			}
		})
	}
}
