# 05 — Docker y entrega

Cumple el requisito 1: *"Deberás entregar un docker con el aplicativo funcionando."*

## Forma de la entrega

Se acordó: **los 64 segmentos, imagen construida aparte del repositorio.**

El repositorio lleva el código y un script que prepara los segmentos desde la carpeta local;
los `.ts` (~500 MB) quedan fuera de Git. Quien evalúa ya tiene los segmentos — se los
entregaron ellos mismos por Drive — así que armar la imagen es un comando.

Un repo de ~500 MB no se puede mandar por correo y obligaría a Drive o a un registry. Esta
forma mantiene el repo liviano sin sacrificar ningún segmento.

## Dockerfile

El archivo real es [../Dockerfile](../Dockerfile) y **no se reproduce acá**: una copia en la
documentación es una copia que envejece, y este proyecto ya tuvo que reconciliar dos veces
documentos que describían código que había cambiado.

Multi-stage. Como SQLite entra por `modernc.org/sqlite`, que es Go puro, se compila con
`CGO_ENABLED=0` y sale un **binario estático**: la imagen final no lleva toolchain de C ni
libc.

Las decisiones y su motivo:

- **`go mod download` antes de copiar el código:** las dependencias quedan en su propia capa
  y no se re-descargan al tocar un `.go`.
- **`-trimpath -ldflags="-s -w"`:** binario más chico y sin las rutas de la máquina de
  compilación.
- **No hay `COPY` de assets web.** Desde el bloque 04, plantillas, CSS, JS y hls.js viajan
  dentro del binario con `go:embed`. El diseño original de este documento sí copiaba un
  directorio `web/`; esa línea rompería el build, porque ese directorio ya no existe.
- **`USER app` (uid 10001, no root):** si algo del servidor se compromete, no es root dentro
  del contenedor. El uid alto evita chocar con uids del host en un bind mount.
- **`VOLUME /data`:** la base sobrevive a `docker rm`. Sin esto, cada recreación borraría los
  usuarios registrados y habría que darse de alta otra vez.
- **`alpine` y no `scratch`:** `scratch` daría una imagen menor, pero sin shell no hay forma
  cómoda de hacer `HEALTHCHECK` ni de inspeccionar el contenedor. Vale los ~8 MB. El `wget`
  del healthcheck es el de BusyBox, ya incluido: no hace falta instalar nada.
- **`ENTRYPOINT` en forma exec, nunca en forma shell.** Con `["/app/server"]` el binario es
  PID 1 y recibe directamente el `SIGTERM` de `docker stop`. En forma shell lo intercepta
  `/bin/sh`, que no lo retransmite, y el contenedor muere de un `SIGKILL` a los 10 segundos:
  las conexiones SSE se cortan de golpe y el WAL de SQLite queda sin cerrar. Por esa misma
  razón `go run` tampoco sirve como proceso principal — se comprobó, y falla igual.

La imagen final pesa **524 MB**, de los cuales 480 son los segmentos. El binario son ~19 MB.

## `.dockerignore`

```text
.git
docs
*.md
*.pdf
hls test/
```

Sin esto, el contexto de build incluiría los 500 MB de la carpeta original **dos veces**.

## Preparación de los segmentos

`scripts/prepare-segments.sh` (y su equivalente `.ps1` para Windows) copia los `.ts` y el
`segment.m3u8` desde la carpeta provista a `./segments/`, verificando que estén los 64.

```bash
./scripts/prepare-segments.sh "../hls test"
docker build -t zapping-live .
docker run -p 8080:8080 -v zapping-data:/data zapping-live
```

Tres comandos, y el README los lista en ese orden.

`segments/` va en `.gitignore` con un `.gitkeep`, para que la carpeta exista pero su
contenido no entre al repositorio.

## Variables de entorno

Todas con defaults que funcionan sin configurar nada.

| Variable | Default | Uso |
| --- | --- | --- |
| `PORT` | `8080` | Puerto HTTP |
| `DB_PATH` | `/data/zapping.db` | Archivo SQLite |
| `SEGMENTS_DIR` | `/app/segments` | Carpeta de segmentos y manifiesto |
| `SESSION_TTL` | `24h` | Vigencia de sesión |
| `SECURE_COOKIES` | `false` | `true` detrás de HTTPS |
| `WINDOW_SIZE` | `3` | Segmentos por playlist (el enunciado fija 3) |

## Arranque y apagado

**Arranque** (`cmd/server/main.go`), en orden y fallando temprano:

1. Leer configuración.
2. Abrir SQLite y aplicar migraciones. Si falla, salir con error claro.
3. Parsear el manifiesto y construir el pool. **Si no hay segmentos, no levantar** — más
   vale un error explícito que un servidor sirviendo 404s.
4. Arrancar la goroutine del motor y la del hub.
5. Arrancar la limpieza periódica de sesiones.
6. Escuchar HTTP.

**Apagado** con `SIGINT`/`SIGTERM`: se cancela el contexto raíz (motor, hub y limpieza
terminan), se llama a `http.Server.Shutdown` con 10s de gracia y se cierra la base. Sin
esto, `docker stop` cortaría conexiones SSE a la fuerza y podría dejar el WAL sin cerrar.

## Verificación antes de entregar

La parte automatizable está en `scripts/smoke.sh`, que levanta la imagen ya construida y
corre las comprobaciones por HTTP. **Verificado el 2026-08-03: 18/18 en verde**, con la
imagen `zapping-live` de 524 MB (480 de ellos son los segmentos).

```bash
./scripts/prepare-segments.sh "hls test"
docker build -t zapping-live .
./scripts/smoke.sh
```

Lo que el script cubre:

- [x] `docker build` termina sin warnings.
- [x] `/healthz` responde 200.
- [x] Sin sesión: el playlist, los segmentos y el SSE dan **401** —no 302—, y `/player`
      redirige al login.
- [x] Las páginas públicas y los estáticos —incluido `hls.min.js`— responden 200.
- [x] El registro crea la cuenta, emite la cookie y redirige.
- [x] Con sesión llega el `.m3u8`, y el segmento que ese playlist nombra se sirve entero.
      Eso ejercita de paso que el playlist y los segmentos sean rutas hermanas.
- [x] El SSE entrega un evento con el contador.
- [x] `MEDIA-SEQUENCE` avanza: el motor está rotando.
- [x] `docker stop` apaga **ordenadamente** —453 ms medidos, de 10 s de gracia— y el log
      dice `apagado limpio`. Si tardara los 10 s completos, la señal no estaría llegando.
- [x] `docker start` conserva el usuario: el login vuelve a funcionar tras reiniciar.

Y aparte, sobre el código:

- [x] `go test ./... -count=1` — 153 tests, 0 fallos.
- [x] `go vet ./...` limpio, `gofmt -l .` sin salida.
- [x] `-race` sin advertencias, en contenedor Linux.

### Lo que sigue siendo manual

No se puede comprobar desde un script, y hay que mirarlo en el navegador:

- [ ] El player reproduce sin cortes durante **al menos 12 minutos**: cubre la vuelta
      completa del ciclo (10,5 min) y el `segment63.ts` de 4,57 s, que es el caso que
      descarta la solución del ticker fijo.
- [ ] Dos pestañas en `/player` → el contador marca 2 en ambas, y vuelve a 1 al cerrar una.
      La cuenta regresiva del panel **no debe saltar hacia atrás** al abrir la segunda.
- [ ] DevTools → Network: cero peticiones a dominios externos. Es lo que verifica que hls.js
      esté de verdad vendorizado.

## Contenido del repositorio entregado

```text
├── cmd/ internal/ web/          código
├── docs/                        estos documentos
├── scripts/prepare-segments.*   preparación de segmentos
├── segments/.gitkeep            (los .ts no van en Git)
├── Dockerfile .dockerignore
├── go.mod go.sum
└── README.md                    qué es, cómo correrlo, decisiones tomadas
```

El README se arma a partir de [06-decisiones.md](06-decisiones.md) e incluye: qué es,
los tres comandos para levantarlo, la arquitectura en un párrafo, las decisiones de diseño
con su justificación, y las ambigüedades detectadas en el enunciado con la interpretación
que se tomó.
