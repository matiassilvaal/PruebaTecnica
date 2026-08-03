package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// tamañoToken en bytes antes de codificar. 32 bytes = 256 bits de entropía.
const tamañoToken = 32

// Sessions persiste las sesiones en la base.
//
// Se implementa acá en vez de usar una librería porque son pocas líneas y
// deja explícita la mecánica. Se eligieron sesiones y no JWT porque se pueden
// REVOCAR al cerrar sesión, cosa que un JWT no permite sin agregar una lista
// de revocación que reintroduce el estado que el JWT venía a evitar.
type Sessions struct {
	db  *sql.DB
	ttl time.Duration
	now func() time.Time
}

type OpcionSesion func(*Sessions)

// ConReloj inyecta el reloj. Los tests lo usan para probar la expiración sin
// esperar el TTL real.
func ConReloj(now func() time.Time) OpcionSesion {
	return func(s *Sessions) { s.now = now }
}

func NewSessions(db *sql.DB, ttl time.Duration, opts ...OpcionSesion) *Sessions {
	s := &Sessions{db: db, ttl: ttl, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// TTL devuelve la vigencia configurada de las sesiones. Es la única fuente de
// verdad para cuánto dura una sesión: Guard.PonerCookie la usa para que la
// cookie y la fila en la base caduquen siempre al mismo tiempo, en vez de
// recibir el TTL por separado y arriesgarse a que diverjan.
func (s *Sessions) TTL() time.Duration { return s.ttl }

// hashToken es la única vía por la que un token se convierte en clave de la
// base. En sessions se guarda SIEMPRE el hash, nunca el token: una filtración
// de la base entrega valores inservibles en vez de sesiones activas.
func hashToken(token string) string {
	suma := sha256.Sum256([]byte(token))
	return hex.EncodeToString(suma[:])
}

// Crear emite un token nuevo. El valor devuelto es el que va en la cookie; en
// la base sólo queda su hash.
func (s *Sessions) Crear(ctx context.Context, userID int64) (string, error) {
	crudo := make([]byte, tamañoToken)
	if _, err := rand.Read(crudo); err != nil {
		return "", fmt.Errorf("generando el token de sesión: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(crudo)

	ahora := s.now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?,?,?,?)`,
		hashToken(token), userID, ahora.Add(s.ttl).Unix(), ahora.Unix())
	if err != nil {
		return "", fmt.Errorf("guardando la sesión: %w", err)
	}
	return token, nil
}

// Resolver devuelve el usuario de una sesión vigente.
//
// La búsqueda es por clave primaria sobre un hash de longitud fija, así que no
// hay riesgo de filtrar información por tiempo de respuesta.
//
// El error de vuelta es DISTINTO de "no encontrado"/"expirado": ese caso
// sigue siendo (0, false, nil), la respuesta normal ante un token que
// simplemente no vale. El error sólo viaja cuando la consulta a la base
// falló de verdad (por ejemplo, SQLite caído). Colapsar ambos casos en un
// mismo (0, false) hacía que RequirePage mandara a /login tanto si el token
// era inválido como si la base no respondía; con la base caída, el usuario
// reintenta, vuelve a fallar, y entra en un bucle de redirección sin que
// quede una sola línea de log indicando la causa real.
func (s *Sessions) Resolver(ctx context.Context, token string) (int64, bool, error) {
	if token == "" {
		return 0, false, nil
	}
	var userID, expira int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`,
		hashToken(token),
	).Scan(&userID, &expira)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolviendo la sesión: %w", err)
	}
	if s.now().Unix() >= expira {
		return 0, false, nil
	}
	return userID, true, nil
}

func (s *Sessions) Destruir(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("destruyendo la sesión: %w", err)
	}
	return nil
}

// DestruirDeUsuario borra todas las sesiones de un usuario. Se llama al
// iniciar sesión para prevenir session fixation.
func (s *Sessions) DestruirDeUsuario(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("destruyendo las sesiones del usuario: %w", err)
	}
	return nil
}

// Limpiar borra las sesiones expiradas y devuelve cuántas eliminó. Sin esto la
// tabla crece indefinidamente: una fuga lenta pero real.
func (s *Sessions) Limpiar(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, fmt.Errorf("limpiando sesiones expiradas: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("contando sesiones borradas: %w", err)
	}
	return n, nil
}
