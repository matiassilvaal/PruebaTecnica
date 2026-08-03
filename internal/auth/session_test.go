package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"zapping-live/internal/storage/storagetest"
)

type relojFijo struct{ t time.Time }

func (r *relojFijo) Now() time.Time          { return r.t }
func (r *relojFijo) Avanzar(d time.Duration) { r.t = r.t.Add(d) }

// nuevasSesiones arma un Sessions sobre una base temporal con un usuario ya
// creado, y devuelve su id. Las sesiones tienen FK contra users.
func nuevasSesiones(t *testing.T, ttl time.Duration) (*Sessions, *relojFijo, int64, context.Context) {
	t.Helper()
	db := storagetest.AbrirMigrada(t)
	ctx := context.Background()
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash, created_at) VALUES (?,?,?,?)`,
		"Ana", "ana@x.com", "h", time.Now().Unix())
	if err != nil {
		t.Fatalf("creando usuario de prueba: %v", err)
	}
	id, _ := res.LastInsertId()
	reloj := &relojFijo{t: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)}
	return NewSessions(db, ttl, ConReloj(reloj.Now)), reloj, id, ctx
}

// resuelve envuelve Resolver para los tests que sólo les importa el "ok":
// exige que err sea nil (un error real de la base no debería ocurrir en
// ninguno de estos escenarios) y devuelve sólo userID y ok.
func resuelve(t *testing.T, s *Sessions, ctx context.Context, token string) (int64, bool) {
	t.Helper()
	userID, ok, err := s.Resolver(ctx, token)
	if err != nil {
		t.Fatalf("Resolver: error inesperado: %v", err)
	}
	return userID, ok
}

func TestTTL(t *testing.T) {
	s, _, _, _ := nuevasSesiones(t, 90*time.Minute)
	if got := s.TTL(); got != 90*time.Minute {
		t.Errorf("TTL() = %v, quiero %v", got, 90*time.Minute)
	}
}

func TestCrearYResolver(t *testing.T) {
	s, _, uid, ctx := nuevasSesiones(t, time.Hour)
	token, err := s.Crear(ctx, uid)
	if err != nil {
		t.Fatalf("Crear: %v", err)
	}
	if len(token) < 40 {
		t.Errorf("el token parece corto (%d chars): %q", len(token), token)
	}
	got, ok, err := s.Resolver(ctx, token)
	if err != nil {
		t.Fatalf("Resolver: %v", err)
	}
	if !ok {
		t.Fatal("el token recién creado debería resolver")
	}
	if got != uid {
		t.Errorf("resolvió al usuario %d, quiero %d", got, uid)
	}
}

func TestTokensDistintosCadaVez(t *testing.T) {
	s, _, uid, ctx := nuevasSesiones(t, time.Hour)
	vistos := map[string]bool{}
	for i := 0; i < 20; i++ {
		tok, err := s.Crear(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if vistos[tok] {
			t.Fatalf("token repetido en la iteración %d: %q", i, tok)
		}
		vistos[tok] = true
	}
}

func TestElTokenNoSeGuardaEnClaro(t *testing.T) {
	s, _, uid, ctx := nuevasSesiones(t, time.Hour)
	token, err := s.Crear(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// Quien se lleve la base debe obtener valores inservibles.
	var guardado string
	if err := s.db.QueryRowContext(ctx,
		`SELECT token_hash FROM sessions LIMIT 1`).Scan(&guardado); err != nil {
		t.Fatal(err)
	}
	if guardado == token || strings.Contains(guardado, token) {
		t.Error("el token se guardó en claro en la base")
	}
}

func TestSesionExpirada(t *testing.T) {
	s, reloj, uid, ctx := nuevasSesiones(t, time.Hour)
	token, err := s.Crear(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	reloj.Avanzar(59 * time.Minute)
	if _, ok := resuelve(t, s, ctx, token); !ok {
		t.Error("dentro del TTL debería seguir resolviendo")
	}
	// Límite exacto: avanzar EXACTAMENTE el TTL (total 60 min) debe expirar
	// la sesión. Resolver compara con `>=`; una mutación a `>` dejaría pasar
	// este caso exacto y sólo lo atraparía este sub-test, no el de arriba
	// (59 min, dentro del TTL) ni el de abajo (61 min, muy pasado el TTL).
	reloj.Avanzar(1 * time.Minute) // total 60 min: exactamente el TTL
	if _, ok := resuelve(t, s, ctx, token); ok {
		t.Error("exactamente en el borde del TTL no debería resolver")
	}
}

func TestSesionExpiradaPasadoElBorde(t *testing.T) {
	s, reloj, uid, ctx := nuevasSesiones(t, time.Hour)
	token, err := s.Crear(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	reloj.Avanzar(61 * time.Minute) // bien pasado el TTL
	if _, ok := resuelve(t, s, ctx, token); ok {
		t.Error("pasado el TTL no debería resolver")
	}
}

func TestResolverTokenInvalido(t *testing.T) {
	s, _, _, ctx := nuevasSesiones(t, time.Hour)
	for _, tok := range []string{"", "inventado", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, ok := resuelve(t, s, ctx, tok); ok {
			t.Errorf("el token %q no debería resolver", tok)
		}
	}
}

func TestDestruir(t *testing.T) {
	s, _, uid, ctx := nuevasSesiones(t, time.Hour)
	token, _ := s.Crear(ctx, uid)
	if err := s.Destruir(ctx, token); err != nil {
		t.Fatalf("Destruir: %v", err)
	}
	if _, ok := resuelve(t, s, ctx, token); ok {
		t.Error("tras Destruir el token no debería resolver")
	}
}

func TestDestruirDeUsuario(t *testing.T) {
	// Es lo que previene session fixation: al iniciar sesión se descartan las
	// anteriores, así un token entregado antes de autenticarse no queda válido.
	//
	// Usa DOS usuarios a propósito: con uno solo, nada distingue un
	// `DELETE FROM sessions WHERE user_id = ?` correcto de un
	// `DELETE FROM sessions` sin WHERE que borrara la tabla entera. Si
	// alguien rompe el WHERE, este test debe notarlo porque las sesiones del
	// otro usuario sobreviven.
	s, _, uid, ctx := nuevasSesiones(t, time.Hour)
	otroID := crearOtroUsuario(t, s, ctx, "Beto", "beto@x.com")

	a, _ := s.Crear(ctx, uid)
	b, _ := s.Crear(ctx, uid)
	deOtro, err := s.Crear(ctx, otroID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DestruirDeUsuario(ctx, uid); err != nil {
		t.Fatalf("DestruirDeUsuario: %v", err)
	}
	if _, ok := resuelve(t, s, ctx, a); ok {
		t.Error("la sesión a debería haber muerto")
	}
	if _, ok := resuelve(t, s, ctx, b); ok {
		t.Error("la sesión b debería haber muerto")
	}
	if _, ok := resuelve(t, s, ctx, deOtro); !ok {
		t.Error("la sesión del OTRO usuario no debería haberse tocado: el WHERE user_id debe acotar el DELETE")
	}
}

// crearOtroUsuario inserta un segundo usuario directamente por SQL (este
// paquete no depende de cuenta.Store) para los tests que necesitan probar
// que un DELETE está acotado a un usuario y no afecta a otro.
func crearOtroUsuario(t *testing.T, s *Sessions, ctx context.Context, name, email string) int64 {
	t.Helper()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, email, password_hash, created_at) VALUES (?,?,?,?)`,
		name, email, "h", time.Now().Unix())
	if err != nil {
		t.Fatalf("creando %s: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestLimpiarBorraSoloLasExpiradas(t *testing.T) {
	s, reloj, uid, ctx := nuevasSesiones(t, time.Hour)
	vieja, _ := s.Crear(ctx, uid)
	reloj.Avanzar(90 * time.Minute) // `vieja` ya expiró
	nueva, _ := s.Crear(ctx, uid)

	n, err := s.Limpiar(ctx)
	if err != nil {
		t.Fatalf("Limpiar: %v", err)
	}
	if n != 1 {
		t.Errorf("Limpiar borró %d filas, quiero 1", n)
	}
	if _, ok := resuelve(t, s, ctx, vieja); ok {
		t.Error("la sesión vieja debería estar borrada")
	}
	if _, ok := resuelve(t, s, ctx, nueva); !ok {
		t.Error("la sesión vigente NO debería haberse borrado")
	}
}
