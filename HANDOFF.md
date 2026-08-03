# Handoff — Prueba técnica Zapping

Documento para retomar este proyecto en una sesión nueva sin contexto previo.
Escrito el 2026-08-03.

> **Antes de entregar a Zapping:** decide si este archivo se queda o se borra.
> Documenta el proceso, no el producto. No molesta, pero tampoco lo pidieron.

---

## 1. Qué es esto

Prueba técnica para Zapping (empresa de streaming). El enunciado íntegro está en
[Prueba.md](Prueba.md); el PDF original, en `Prueba v3. (1).pdf`.

**Resumen:** un servicio en Go que sirve un **livestreaming HLS simulado** a partir de 64
segmentos `.ts` pregrabados, con registro de usuarios y un player web al que sólo entran
cuentas registradas. Se entrega como imagen Docker.

**Lo que evalúan explícitamente:** orden de código, buen uso de funciones asíncronas y
síncronas, estructura de datos, y manejo de memoria RAM. Cada decisión del diseño está
justificada contra alguno de esos cuatro criterios.

---

## 2. Estado actual

### Ramas (apiladas, sin mergear a propósito)

```text
master          c3e685b   sólo documentación de diseño
  └── feat/motor-hls   90d8a0d   bloque 02 — COMPLETO
        └── feat/auth-db   0c3474f   bloque 03 — COMPLETO   ← rama activa
```

**Decisión del usuario:** las ramas se apilan y **el merge de todo a `master` se hace al
final**, cuando él haya corrido sus pruebas manuales end-to-end. No mergees nada sin que lo
pida.

### Qué está construido

| Paquete | Qué hace | Tests |
| --- | --- | --- |
| `internal/hls` | Motor del livestream: pool, tabla de duraciones acumuladas, snapshot inmutable, bucle de rotación | 35 |
| `internal/storage` | SQLite (modernc, Go puro), pragmas, migraciones idempotentes | 9 |
| `internal/cuenta` | Modelo `Usuario`, validación, CRUD, `Registrar` | 15 |
| `internal/auth` | bcrypt, sesiones, `Guard` con `RequirePage`/`RequireAPI` | 25 |

**84 tests, todos en verde.** Verificar con:

```bash
go test ./... -count=1
CGO_ENABLED=0 go build ./...     # debe compilar: habilita el binario estático
go vet ./... && gofmt -l .
```

### Qué falta

- **Bloque 04** — web y frontend: handlers HTTP, hub SSE, las tres páginas, player con
  hls.js, panel en vivo con glassmorfismo. Diseño en [docs/04-web-y-frontend.md](docs/04-web-y-frontend.md).
  **Es el bloque más grande.**
- **Bloque 05** — Docker y entrega. Diseño en [docs/05-docker-y-entrega.md](docs/05-docker-y-entrega.md).
- **README.md** — no existe todavía. Se arma desde [docs/06-decisiones.md](docs/06-decisiones.md).
- **Correo de entrega** — con copia a Nacho y Claudio, justificando el tiempo invertido.

---

## 3. Cómo se está trabajando

El proyecto usa el plugin **superpowers**. El flujo por bloque es:

1. `superpowers:brainstorming` → acuerda el diseño (ya hecho para los cuatro bloques,
   están en `docs/01` a `docs/05`).
2. `superpowers:writing-plans` → plan de implementación TDD, tarea por tarea, en
   `docs/plans/`.
3. `superpowers:subagent-driven-development` → ejecuta el plan: un subagente implementa
   cada tarea, otro la revisa, y hay ronda de corrección si hay hallazgos. Al final, una
   revisión de la rama completa.

**Los planes existentes son buenos modelos:** [docs/plans/2026-08-02-motor-hls.md](docs/plans/2026-08-02-motor-hls.md)
y [docs/plans/2026-08-02-auth-y-db.md](docs/plans/2026-08-02-auth-y-db.md). Cada tarea trae
el código y los tests completos, no descripciones.

**Convenciones que ya están asentadas y hay que respetar:**

- Comentarios, mensajes de error y nombres de test **en español**.
- Los comentarios explican **por qué**, no qué. Los `// X existe para que...` del código
  actual son el estándar.
- Commits en español, con cuerpo que explique la decisión cuando no sea obvia.
- Cada tarea del plan termina con su propio commit.

---

## 4. Decisiones ya tomadas — no re-litigar

El usuario las eligió explícitamente. Cambiar alguna requiere preguntarle.

### Producto y alcance

- **Alcance:** sólido y profesional, con **una** feature opcional: contador de espectadores
  en vivo + panel de estado del stream, vía SSE.
- **Los 64 segmentos**, con imagen Docker construida aparte del repositorio (los `.ts` no
  van en Git; están en `hls test/` y ya en `.gitignore`).
- **Frontend con glassmorfismo, sin Bootstrap.** hls.js vendorizado, no por CDN.
- **Regla estricta:** nunca `backdrop-filter` encima del `<video>` — recalcula el desenfoque
  a 25-30 fps en GPU y puede tirar frames del propio video. Vidrio al lado, no encima.

### Técnicas

- **Stack:** Go stdlib (`net/http` con routing nativo 1.22+), SQLite vía `modernc.org/sqlite`
  (Go puro, sin cgo), sesiones propias en DB, bcrypt costo 12.
- **Exactamente dos dependencias externas:** `modernc.org/sqlite` y `golang.org/x/crypto`.
  No agregues una tercera sin preguntar.
- **El motor de live deriva la posición del reloj monotónico** contra una tabla de duraciones
  acumuladas; no incrementa un contador. Eso hace la deriva imposible y soporta el segmento
  de 4,566667s sin caso especial.
- **El estado del stream no se muta:** se publica un `Snapshot` inmutable con
  `atomic.Pointer`. Los lectores hacen `Load` sin locks.
- **Fechas en SQLite como `INTEGER`** con segundos Unix, nunca `DATETIME`.
- **Dos middlewares explícitos** (`RequirePage` → 302, `RequireAPI` → 401), no uno que
  inspeccione `Accept: text/html`.

---

## 5. Restricciones que el bloque 04 hereda — CRÍTICO

Salieron de las revisiones de los bloques 02 y 03. Están también en
[docs/06-decisiones.md](docs/06-decisiones.md), sección "Restricciones que heredan los
bloques siguientes". Si el bloque 04 las ignora, rompe cosas que ya funcionan.

**Del motor HLS:**

- `Engine.Run` debe llamarse **exactamente una vez** por instancia. La garantía de un solo
  escritor es convención documentada, no forzada por código: dos goroutines rompen la
  monotonía de `EXT-X-MEDIA-SEQUENCE`.
- El hook `onRotate` corre **síncronamente en la goroutine de rotación**. El hub SSE debe
  reenviar a su propio canal sin bloquear: si bloquea, detiene el stream para todos; si
  entra en pánico, tumba la goroutine del motor.
- **El playlist debe servirse desde una ruta hermana de `segments/`** — `/live/stream.m3u8`
  con los segmentos en `/live/segments/`. Las URI del `.m3u8` son relativas; montarlo en
  otra ruta da 404 silenciosos.
- `Snapshot.Window` y `Snapshot.Playlist` son de **sólo lectura**. Comparten el array
  subyacente: mutarlos corrompe lo que ven todos los lectores.

**De auth y DB:**

- **El alta va por `cuenta.Registrar`, no por `Store.Crear`.** `Crear` recibe el hash ya
  calculado y no valida; usarlo desde un handler permitiría guardar la contraseña en claro.
- **El handler de login DEBE llamar a `auth.VerificarEnVacio()` cuando el email no existe.**
  Está implementada y probada pero **no tiene llamante todavía**. Sin ella, un email
  inexistente responde en microsegundos y uno registrado paga ~370 ms de bcrypt: el tiempo
  revela qué cuentas existen aunque el mensaje sea idéntico.
- **El login debe rotar la sesión** (`DestruirDeUsuario` antes de `Crear`), contra session
  fixation. Nota: eso desconecta los demás dispositivos del usuario, discutible en un
  producto de streaming — si molesta, rotar sólo el token.
- **`Sessions.Limpiar` no la ejecuta nadie.** Debe correr en una goroutine periódica
  cancelable por contexto, o la tabla `sessions` crece sin límite. Es además el único
  elemento asíncrono que aporta este bloque, y la asincronía es criterio evaluado.
- **`Sessions.Resolver` devuelve `(int64, bool, error)`.** Ignorar el tercer valor produce
  un bucle de redirección al login sin logs si SQLite se cae.
- **La cookie se emite con `Guard.PonerCookie(w, token)`**, que toma el TTL de `Sessions`.
  No pases el TTL por separado.

**Optimización conocida y no aplicada:** `Guard.proteger` hace dos consultas por request
(`sessions` y luego `users`). En `/live/segments/{name}` son dos por segmento y por
espectador. Un `JOIN` las reduce a una. Está documentada en `docs/06`.

---

## 6. Trampas del entorno

- **`go test -race` NO funciona en esta máquina.** No hay compilador C y en Windows el
  detector lo requiere. Se verifica en contenedor Linux:

  ```bash
  docker run --rm -v "C:/Users/Matias/Desktop/PruebaTecnica:/src" -w /src \
    golang:1.26 go test -race ./...
  ```

  (Con Git Bash hace falta `MSYS_NO_PATHCONV=1` delante.) La imagen `golang:1.26` ya está
  descargada. Docker 20.10.17; el daemon hay que abrirlo a mano.

- **El antivirus bloquea de forma intermitente** la ejecución de binarios de test recién
  compilados: `fork/exec ...\user.test.exe: Access is denied`. Es aleatorio y poco frecuente.
  Rodeo fiable:

  ```bash
  go test -c -o pruebas.exe ./internal/paquete/ && (cd internal/paquete && ./pruebas.exe)
  ```

  Ya se resolvió un problema relacionado: los helpers de test crean su propio directorio
  temporal y lo borran con reintentos, porque `testing.removeAll` no reintenta ante
  `ERROR_DIR_NOT_EMPTY` (Defender abre el archivo al cerrarse SQLite).

- **`git checkout --` reintroduce CRLF** por `core.autocrlf=true`, y entonces `gofmt -l`
  marca el archivo. Si pasa, normaliza a LF.

- Entorno: Go 1.26.5, Windows 10, Git Bash y PowerShell 5.1 disponibles.

---

## 7. Lo que el proceso ha enseñado

Vale la pena saberlo porque se repitió en los dos bloques:

**La mayoría de los hallazgos importantes estaban en los planes, no en la implementación.**
Los subagentes transcribieron fielmente lo que se les pidió; el defecto venía de más arriba.
Cuando un revisor encuentre algo, comprueba si el plan lo mandaba — y si es así, corrige
también el plan para que no se propague.

**Casi todos los hallazgos fueron tests que mentían, no bugs de lógica.** Ejemplos reales de
este proyecto:

- Un test decía cubrir la asimetría runas/bytes de la contraseña; mutar el conteo a bytes no
  lo rompía.
- `TestSesionHuerfanaNoPasa` no ejercitaba la rama que decía probar: el `ON DELETE CASCADE`
  borraba la sesión antes de llegar ahí.
- El límite exacto de expiración no estaba cubierto: cambiar `>=` por `>` no rompía nada.

**Por eso: pide a los revisores que muten el código deliberadamente** y comprueben si algún
test se queja. Es lo que encontró casi todo.

**Y verifica lo commiteado, no el working tree.** Una tarea reportó `go mod tidy` como hecho
verificando con `cat`; el cambio nunca se commiteó. Usa `git show <commit>:<archivo>`.

---

## 8. Cómo retomar

```bash
cd c:/Users/Matias/Desktop/PruebaTecnica
git checkout feat/auth-db          # rama activa, todo verde
go test ./... -count=1             # confirmar el punto de partida
```

Luego, para el bloque 04:

1. Crear la rama: `git checkout -b feat/web-frontend` (encima de `feat/auth-db`).
2. Leer [docs/04-web-y-frontend.md](docs/04-web-y-frontend.md), que es el diseño acordado.
3. Invocar `superpowers:writing-plans` para escribir
   `docs/plans/AAAA-MM-DD-web-y-frontend.md`, siguiendo el formato de los dos planes que ya
   existen.
4. Invocar `superpowers:subagent-driven-development` para ejecutarlo.
5. Al cerrar, actualizar el estado del bloque en [docs/00-indice.md](docs/00-indice.md) y
   registrar en [docs/06-decisiones.md](docs/06-decisiones.md) lo que herede el bloque 05.

**Antes de empezar a planificar, revisa `docs/04-web-y-frontend.md` contra las restricciones
de la sección 5 de este documento.** El diseño se escribió antes de que existieran los
bloques 02 y 03, así que puede contradecirlos en algún punto — igual que pasó con el doc 03,
que hubo que reconciliar. Los puntos a verificar: cómo el hub SSE recibe los snapshots (es un
hook síncrono, no un canal), y que el handler de login use `Registrar` y `VerificarEnVacio`.

---

## 9. Ambigüedades del enunciado, ya interpretadas

Están detalladas en [docs/06-decisiones.md](docs/06-decisiones.md) y deben mencionarse en el
correo de entrega: es la señal más barata de que el enunciado se leyó con atención.

1. La sección B dice "el Livestreaming generado en **NodeJS**" — residuo de una versión
   anterior; el resto del enunciado dice Go inequívocamente.
2. El ejemplo de `.m3u8` usa 6s y 4 segmentos, pero el texto pide 10s y 3. Manda el texto.
3. "eliminar el **último** segmento (primero de la lista)" se contradice; el paréntesis
   aclara que es FIFO.
4. El enunciado fija la ventana en 3 pero no dice cuántos `.ts` debe haber en disco; exige
   implícitamente más de 3 al pedir "agregar un segmento **nuevo**".
