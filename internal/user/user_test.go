package user

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizarEmail(t *testing.T) {
	casos := map[string]string{
		"  Ana@X.COM  ": "ana@x.com",
		"ana@x.com":     "ana@x.com",
		"ANA@X.COM":     "ana@x.com",
	}
	for entrada, quiero := range casos {
		if got := NormalizarEmail(entrada); got != quiero {
			t.Errorf("NormalizarEmail(%q) = %q, quiero %q", entrada, got, quiero)
		}
	}
}

func TestValidarAceptaDatosBuenos(t *testing.T) {
	if err := Validar("Ana Pérez", "ana@x.com", "unaclavelarga"); err != nil {
		t.Errorf("quiero nil, tengo %v", err)
	}
}

func TestValidarRechaza(t *testing.T) {
	casos := []struct {
		nombre            string
		name, email, pass string
		campo             string
	}{
		{"nombre vacío", "", "ana@x.com", "unaclavelarga", "nombre"},
		{"nombre sólo espacios", "   ", "ana@x.com", "unaclavelarga", "nombre"},
		{"nombre muy largo", strings.Repeat("a", 101), "ana@x.com", "unaclavelarga", "nombre"},
		{"email vacío", "Ana", "", "unaclavelarga", "email"},
		{"email sin arroba", "Ana", "anax.com", "unaclavelarga", "email"},
		{"email sin dominio", "Ana", "ana@", "unaclavelarga", "email"},
		{"email muy largo", "Ana", strings.Repeat("a", 250) + "@x.com", "unaclavelarga", "email"},
		{"contraseña corta", "Ana", "ana@x.com", "1234567", "contraseña"},
		{"contraseña vacía", "Ana", "ana@x.com", "", "contraseña"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := Validar(c.name, c.email, c.pass)
			if err == nil {
				t.Fatal("quiero error, tengo nil")
			}
			var ev ErrorValidacion
			if !errors.As(err, &ev) {
				t.Fatalf("quiero ErrorValidacion, tengo %T", err)
			}
			if ev.Campo != c.campo {
				t.Errorf("Campo = %q, quiero %q", ev.Campo, c.campo)
			}
			if ev.Mensaje == "" {
				t.Error("el mensaje no puede estar vacío: se muestra al usuario")
			}
		})
	}
}

func TestValidarPasswordMuyLargaSeRechaza(t *testing.T) {
	// bcrypt TRUNCA silenciosamente en 72 bytes: sin este límite, dos
	// contraseñas que compartan los primeros 72 bytes serían equivalentes.
	larga := strings.Repeat("a", 73)
	err := Validar("Ana", "ana@x.com", larga)
	if err == nil {
		t.Fatal("quiero error para una contraseña de 73 bytes")
	}
	var ev ErrorValidacion
	if errors.As(err, &ev) && ev.Campo != "contraseña" {
		t.Errorf("Campo = %q, quiero \"contraseña\"", ev.Campo)
	}
}

func TestValidarLimiteEnBytesNoEnRunas(t *testing.T) {
	// 40 emojis de 4 bytes = 160 bytes: son sólo 40 caracteres pero bcrypt
	// igual truncaría. El límite alto se mide en BYTES.
	if err := Validar("Ana", "ana@x.com", strings.Repeat("🔒", 40)); err == nil {
		t.Fatal("quiero error: 160 bytes supera el límite de bcrypt")
	}
	// 8 emojis = 32 bytes, 8 caracteres: válida. El mínimo se mide en RUNAS,
	// porque es una regla de cara al usuario.
	if err := Validar("Ana", "ana@x.com", strings.Repeat("🔒", 8)); err != nil {
		t.Errorf("8 caracteres deberían bastar, tengo %v", err)
	}
	// 5 emojis = 5 runas (< 8, debe RECHAZARSE) pero 20 bytes (> 8: una
	// implementación que comparara bytes en vez de runas lo ACEPTARÍA). Este
	// es el caso que de verdad distingue "en runas" de "en bytes": los dos
	// casos de arriba (8 emojis) satisfacen ambos criterios por igual y no
	// detectarían una mutación de RuneCountInString a len().
	if err := Validar("Ana", "ana@x.com", strings.Repeat("🔒", 5)); err == nil {
		t.Fatal("quiero error: 5 runas no llegan al mínimo, aunque sean 20 bytes")
	}
}

func TestValidarRechazaEmailConEnvoltorio(t *testing.T) {
	// mail.ParseAddress acepta estas formas porque son válidas como
	// "dirección de correo con nombre" (RFC 5322), pero para nosotros el
	// email es sólo la dirección desnuda: si aceptáramos el envoltorio,
	// NormalizarEmail("Nombre <ana@x.com>") != NormalizarEmail("ana@x.com"),
	// y dos altas para la misma persona no colisionarían en el UNIQUE de la
	// base ni permitirían el mismo login.
	casos := []string{
		"Nombre <ana@x.com>",
		"<ana@x.com>",
		"ana@x.com (comentario)",
		"ana@ X.COM",
	}
	for _, email := range casos {
		t.Run(email, func(t *testing.T) {
			err := Validar("Ana", email, "unaclavelarga")
			if err == nil {
				t.Fatalf("quiero error para %q: no debe aceptarse ningún envoltorio alrededor de la dirección", email)
			}
			var ev ErrorValidacion
			if !errors.As(err, &ev) {
				t.Fatalf("quiero ErrorValidacion, tengo %T", err)
			}
			if ev.Campo != "email" {
				t.Errorf("Campo = %q, quiero \"email\"", ev.Campo)
			}
		})
	}
	// Control positivo: la dirección desnuda debe seguir aceptándose.
	if err := Validar("Ana", "ana@x.com", "unaclavelarga"); err != nil {
		t.Errorf("la dirección desnuda no debería rechazarse: %v", err)
	}
}
