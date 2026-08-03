package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Los tests hashean con MinCost: con el costo 12 de producción cada hash
// tarda ~250 ms y la suite se volvería lenta de correr.
func hashRapido(t *testing.T, plain string) string {
	t.Helper()
	h, err := hashConCosto(plain, bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashConCosto: %v", err)
	}
	return h
}

func TestVerifyPassword(t *testing.T) {
	h := hashRapido(t, "correcta")
	if !VerifyPassword(h, "correcta") {
		t.Error("la contraseña correcta debería validar")
	}
	if VerifyPassword(h, "incorrecta") {
		t.Error("una contraseña distinta NO debería validar")
	}
	if VerifyPassword(h, "") {
		t.Error("la contraseña vacía no debería validar")
	}
}

func TestHashDistintoCadaVez(t *testing.T) {
	// bcrypt genera una sal nueva por hash: dos hashes de la misma contraseña
	// deben diferir, o la base filtraría qué usuarios comparten clave.
	a, b := hashRapido(t, "misma"), hashRapido(t, "misma")
	if a == b {
		t.Error("dos hashes de la misma contraseña son idénticos: no hay sal")
	}
	if !VerifyPassword(a, "misma") || !VerifyPassword(b, "misma") {
		t.Error("ambos hashes deberían validar la misma contraseña")
	}
}

func TestHashNoContieneLaContraseña(t *testing.T) {
	h := hashRapido(t, "supersecreta")
	if strings.Contains(h, "supersecreta") {
		t.Errorf("la contraseña aparece en el hash: %q", h)
	}
}

func TestVerifyPasswordHashInvalido(t *testing.T) {
	// Un hash corrupto debe devolver false, no panickear.
	for _, h := range []string{"", "no-es-un-hash", "$2a$rot0"} {
		if VerifyPassword(h, "x") {
			t.Errorf("el hash inválido %q no debería validar", h)
		}
	}
}

// TestVerificarEnVacioUsaCostoDeProduccion no mide tiempos: un umbral de
// tiempo es inevitablemente frágil en CI (máquinas compartidas, sandboxes
// virtualizados, GC del runtime) y un falso rojo intermitente entrena a
// ignorar la suite, que es peor que no tener el test. En cambio verifica
// directamente lo que hace que la mitigación funcione: que el hash de
// referencia contra el que compara sea de costo CostoBcrypt (12), el mismo
// costo que paga una verificación real. Si alguien lo bajara a MinCost "para
// que los tests vayan rápido", VerificarEnVacio dejaría de disimular nada, y
// este test lo atraparía sin depender del reloj.
func TestVerificarEnVacioUsaCostoDeProduccion(t *testing.T) {
	costo, err := bcrypt.Cost([]byte(hashReferencia))
	if err != nil {
		t.Fatalf("leyendo el costo del hash de referencia: %v", err)
	}
	if costo != CostoBcrypt {
		t.Errorf("hash de referencia con costo %d, quiero %d", costo, CostoBcrypt)
	}
	// No debe panicar ni devolver true por accidente contra una contraseña
	// que no coincide con nada.
	VerificarEnVacio()
}

func TestCostoDeProduccionEs12(t *testing.T) {
	// Este es el ÚNICO test que paga el costo real, para dejar constancia de
	// que la constante de producción es la que se cree.
	if CostoBcrypt != 12 {
		t.Fatalf("CostoBcrypt = %d, quiero 12", CostoBcrypt)
	}
	h, err := HashPassword("clave-de-produccion")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	costo, err := bcrypt.Cost([]byte(h))
	if err != nil {
		t.Fatalf("leyendo el costo del hash: %v", err)
	}
	if costo != 12 {
		t.Errorf("el hash quedó con costo %d, quiero 12", costo)
	}
	if !VerifyPassword(h, "clave-de-produccion") {
		t.Error("el hash de producción debería validar")
	}
}
