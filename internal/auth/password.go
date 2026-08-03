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

// hashReferencia se genera UNA sola vez, al cargar el paquete: si se generara
// en cada llamada a VerificarEnVacio, el costo de un login con email
// inexistente sería el DOBLE del de uno válido (generar el hash de
// referencia además de compararlo), lo que reintroduciría exactamente la
// asimetría de tiempos que esta función existe para evitar.
var hashReferencia = func() string {
	h, err := hashConCosto("contraseña-de-referencia-para-igualar-el-costo-de-bcrypt", CostoBcrypt)
	if err != nil {
		// No hay nada razonable que hacer con este error: si bcrypt no puede
		// generar NINGÚN hash al cargar el paquete, el resto de auth
		// (registro, login) ya está inutilizable.
		panic(fmt.Sprintf("auth: generando el hash de referencia: %v", err))
	}
	return h
}()

// VerificarEnVacio ejecuta una comparación bcrypt contra un hash de referencia,
// para que un login con email inexistente tarde lo mismo que uno con email
// válido. Sin esto, el tiempo de respuesta revela qué emails están registrados
// aunque el mensaje de error sea idéntico: un email inexistente responde en
// microsegundos (nunca llega a bcrypt) y uno existente paga el costo real de
// CompareHashAndPassword (~250 ms con CostoBcrypt=12).
//
// El handler de login debe llamarla en la rama "el email no existe", en vez
// de simplemente devolver el mensaje genérico de inmediato.
func VerificarEnVacio() {
	VerifyPassword(hashReferencia, "contraseña-de-relleno-que-nunca-va-a-coincidir")
}
