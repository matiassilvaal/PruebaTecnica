// Package cuenta modela a los usuarios registrados y persiste su alta.
package cuenta

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxNombre        = 100
	MaxEmail         = 254 // longitud máxima de una dirección según RFC 5321
	MinPassword      = 8   // en runas: es una regla de cara al usuario
	MaxPasswordBytes = 72  // en bytes: es el límite técnico de bcrypt
)

// Usuario es un usuario registrado. NO tiene campo de contraseña: el hash
// viaja aparte, sólo hasta donde hace falta verificarlo, así es imposible
// filtrarlo al renderizar un template o serializar a JSON.
type Usuario struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
}

// ErrorValidacion identifica qué campo falló y con qué mensaje, para que el
// handler pueda re-renderizar el formulario señalando el campo correcto.
type ErrorValidacion struct {
	Campo   string
	Mensaje string
}

func (e ErrorValidacion) Error() string {
	return fmt.Sprintf("%s: %s", e.Campo, e.Mensaje)
}

// NormalizarEmail deja el email en su forma canónica. Debe aplicarse SIEMPRE
// antes de guardar o buscar, o el UNIQUE de la base dejaría pasar
// "Ana@x.com" junto a "ana@x.com".
func NormalizarEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// Validar aplica las reglas de alta. Vive acá y no en los handlers para que
// sea la misma desde cualquier punto de entrada.
func Validar(name, email, password string) error {
	if strings.TrimSpace(name) == "" {
		return ErrorValidacion{"nombre", "El nombre es obligatorio"}
	}
	if utf8.RuneCountInString(name) > MaxNombre {
		return ErrorValidacion{"nombre",
			fmt.Sprintf("El nombre no puede superar los %d caracteres", MaxNombre)}
	}

	normalizado := NormalizarEmail(email)
	if normalizado == "" {
		return ErrorValidacion{"email", "El email es obligatorio"}
	}
	if len(normalizado) > MaxEmail {
		return ErrorValidacion{"email", "El email es demasiado largo"}
	}
	// mail.ParseAddress no basta: acepta formas como "Nombre <ana@x.com>",
	// "<ana@x.com>" o "ana@x.com (comentario)", y las deja pasar sin avisar.
	// Si guardaramos esas formas, NormalizarEmail("Nombre <ana@x.com>") no
	// colisionaría con NormalizarEmail("ana@x.com") en el UNIQUE de la base,
	// permitiendo altas duplicadas y rompiendo el login cruzado. Por eso
	// exigimos que la dirección ya extraída (.Address) coincida con lo que
	// el usuario mandó: eso descarta cualquier envoltorio.
	addr, err := mail.ParseAddress(normalizado)
	if err != nil || addr.Address != normalizado {
		return ErrorValidacion{"email", "El email no es válido"}
	}

	if utf8.RuneCountInString(password) < MinPassword {
		return ErrorValidacion{"contraseña",
			fmt.Sprintf("La contraseña debe tener al menos %d caracteres", MinPassword)}
	}
	// bcrypt trunca en 72 bytes sin avisar. Rechazar es preferible a que dos
	// contraseñas distintas terminen siendo la misma.
	if len(password) > MaxPasswordBytes {
		return ErrorValidacion{"contraseña",
			fmt.Sprintf("La contraseña no puede superar los %d bytes", MaxPasswordBytes)}
	}
	return nil
}
