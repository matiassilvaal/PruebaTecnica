// Package auth maneja contraseñas, sesiones y el acceso a las rutas protegidas.
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// CostoBcrypt es el factor de trabajo en producción: unos ~250 ms por
// verificación en hardware moderno, el equilibrio recomendado entre
// resistencia a fuerza bruta y latencia tolerable en un login.
const CostoBcrypt = 12

// HashPassword devuelve el hash a guardar. bcrypt genera la sal y codifica el
// costo DENTRO del propio string, así que basta una columna en la base.
func HashPassword(plain string) (string, error) {
	return hashConCosto(plain, CostoBcrypt)
}

// hashConCosto existe para que los tests puedan usar bcrypt.MinCost: con el
// costo de producción, una suite con varios hashes tardaría segundos.
func hashConCosto(plain string, costo int) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), costo)
	if err != nil {
		return "", fmt.Errorf("hasheando la contraseña: %w", err)
	}
	return string(h), nil
}

// VerifyPassword indica si la contraseña corresponde al hash. Devuelve false
// también ante un hash corrupto: quien llama sólo necesita saber si entra.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
