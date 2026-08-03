package cuenta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
)

var (
	// ErrEmailEnUso permite al handler mostrar un mensaje útil en vez de un 500.
	ErrEmailEnUso   = errors.New("ya existe una cuenta con ese email")
	ErrNoEncontrado = errors.New("usuario no encontrado")
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Crear da de alta un usuario. `hash` ya viene hasheado: este paquete no sabe
// nada de bcrypt, sólo persiste lo que le den.
func (s *Store) Crear(ctx context.Context, name, email, hash string) (*Usuario, error) {
	normalizado := NormalizarEmail(email)
	ahora := time.Now()

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash, created_at) VALUES (?,?,?,?)`,
		name, normalizado, hash, ahora.Unix())
	if err != nil {
		if esViolacionDeUnicidad(err) {
			return nil, ErrEmailEnUso
		}
		return nil, fmt.Errorf("creando usuario: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("obteniendo el id del usuario: %w", err)
	}
	return &Usuario{
		ID:        id,
		Name:      name,
		Email:     normalizado,
		CreatedAt: ahora.Truncate(time.Second),
	}, nil
}

// PorEmail devuelve el usuario y su hash por separado. El hash NO va dentro de
// Usuario a propósito: así no puede filtrarse al renderizar o serializar.
func (s *Store) PorEmail(ctx context.Context, email string) (*Usuario, string, error) {
	var (
		u      Usuario
		hash   string
		creado int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, password_hash, created_at FROM users WHERE email = ?`,
		NormalizarEmail(email),
	).Scan(&u.ID, &u.Name, &u.Email, &hash, &creado)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNoEncontrado
	}
	if err != nil {
		return nil, "", fmt.Errorf("buscando usuario por email: %w", err)
	}
	u.CreatedAt = time.Unix(creado, 0)
	return &u, hash, nil
}

func (s *Store) PorID(ctx context.Context, id int64) (*Usuario, error) {
	var (
		u      Usuario
		creado int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Name, &u.Email, &creado)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoEncontrado
	}
	if err != nil {
		return nil, fmt.Errorf("buscando usuario por id: %w", err)
	}
	u.CreatedAt = time.Unix(creado, 0)
	return &u, nil
}

// sqliteConstraintUnique es SQLITE_CONSTRAINT_UNIQUE, el código extendido que
// devuelve SQLite cuando un INSERT choca contra un índice UNIQUE.
const sqliteConstraintUnique = 2067

// esViolacionDeUnicidad distingue el email duplicado de cualquier otro fallo,
// para poder devolver ErrEmailEnUso y que el handler muestre un mensaje útil
// en vez de un 500.
//
// Se comprueba por CÓDIGO y no por texto: el mensaje del driver puede cambiar
// entre versiones, el código está fijado por SQLite.
func esViolacionDeUnicidad(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) && serr.Code() == sqliteConstraintUnique
}
