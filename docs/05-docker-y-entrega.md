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

Multi-stage. Como SQLite es Go puro, se compila con `CGO_ENABLED=0` y sale un **binario
estático**, lo que permite una imagen final sin toolchain de C ni libc.

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download                      # capa cacheada: no se reconstruye al tocar código
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app && apk add --no-cache wget
WORKDIR /app
COPY --from=build /out/server .
COPY web/ ./web/
COPY segments/ ./segments/               # los 64 .ts + segment.m3u8
RUN mkdir -p /data && chown -R app /data
USER app
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/app/server"]
```

Decisiones y su motivo:

- **`go mod download` antes de copiar el código:** las dependencias quedan en una capa
  aparte y no se re-descargan en cada build.
- **`-trimpath -ldflags="-s -w"`:** binario más chico y sin rutas de la máquina de compilación.
- **`USER app` (no root):** si algo del servidor se compromete, no es root en el contenedor.
- **`VOLUME /data`:** la base sobrevive a `docker rm`. Sin esto, cada reinicio borra los
  usuarios registrados y el evaluador tendría que registrarse de nuevo cada vez.
- **`alpine` y no `scratch`:** `scratch` daría una imagen menor, pero sin shell no hay forma
  cómoda de hacer `HEALTHCHECK` ni de inspeccionar el contenedor. Vale los ~8 MB.

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

Checklist manual, sobre el contenedor ya construido:

- [ ] `docker build` termina sin warnings.
- [ ] `docker run` levanta y `/healthz` responde 200.
- [ ] Registrar un usuario, cerrar sesión, volver a entrar.
- [ ] El player reproduce sin cortes durante **al menos 12 minutos** — cubre la vuelta
      completa del ciclo (10,5 min) y el segmento de 4,57s.
- [ ] Dos pestañas → el contador marca 2.
- [ ] `docker stop` y `docker start` conservan el usuario registrado.
- [ ] Con la red del contenedor deshabilitada, el frontend sigue funcionando (verifica que
      no quedó ningún CDN).
- [ ] `curl` al `.m3u8` sin cookie → 401.
- [ ] `go test ./...` en verde.
- [ ] `go vet ./...` limpio.

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
