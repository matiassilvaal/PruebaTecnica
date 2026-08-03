# Imagen del servicio de livestreaming. Multi-stage: se compila con el toolchain
# de Go y se entrega sólo el binario.
#
# SQLite entra por modernc.org/sqlite, que es Go puro, así que se puede compilar
# con CGO_ENABLED=0 y sale un binario estático. Eso es lo que permite que la
# imagen final no lleve toolchain de C ni libc.

# --platform=$BUILDPLATFORM hace que esta etapa corra SIEMPRE en la arquitectura
# de quien construye, y el binario se cross-compila hacia la de destino con
# GOOS/GOARCH. Como CGO_ENABLED=0, Go cross-compila sin toolchain adicional.
#
# La alternativa —dejar que Docker emule también la compilación— tarda un orden
# de magnitud más sin ganar nada. Importa de verdad: un Mac con Apple Silicon
# necesita linux/arm64, y una imagen amd64 ahí sólo corre emulada.
#
# La etapa final sí es de la arquitectura de destino y ejecuta dos instrucciones,
# así que construir para otra arquitectura necesita los manejadores binfmt.
# Docker Desktop los trae; en un Linux pelado se instalan con
#   docker run --privileged --rm tonistiigi/binfmt --install all
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Las dependencias van en su propia capa, antes del código: tocar un .go no
# vuelve a descargarlas.
COPY go.mod go.sum ./
RUN go mod download

# Sólo lo que el compilador necesita. Un `COPY . .` arrastraría los ~480 MB de
# segments/ a la etapa de build, que no los toca: engorda la caché del builder y
# hace que volver a preparar los segmentos invalide la capa de compilación de Go
# por una razón que no tiene que ver con Go. Los assets web sí entran, porque
# van embebidos: viven bajo internal/web/.
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# -trimpath borra las rutas de la máquina de compilación del binario.
# -s -w quitan la tabla de símbolos y la información de depuración: ~30 % menos
# de tamaño, y nada de esto hace falta en producción.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server


FROM alpine:3.20

# Usuario sin privilegios. Si algo del servidor se compromete, no es root dentro
# del contenedor. El uid alto evita chocar con uids del host en un bind mount.
RUN adduser -D -u 10001 app

WORKDIR /app

COPY --from=build /out/server ./server

# Los .ts y su manifiesto. NO hay COPY de assets web: desde el bloque 04 las
# plantillas, el CSS, el JS y hls.js viajan dentro del binario con go:embed, así
# que el contenedor no depende de ningún directorio del host ni del working dir.
COPY segments/ ./segments/

# La base vive en un volumen para que sobreviva a `docker rm`. Sin esto, cada
# recreación del contenedor borraría los usuarios registrados y habría que
# volver a darse de alta.
RUN mkdir -p /data && chown app:app /data
VOLUME /data

USER app
EXPOSE 8080

# El healthcheck consulta /healthz, que a su vez toca la base. Un 200
# incondicional haría que Docker reiniciara contenedores sanos y dejara vivos
# los rotos. wget es el de BusyBox, ya incluido en alpine: no hace falta instalar
# nada.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- "http://localhost:${PORT:-8080}/healthz" || exit 1

# FORMA EXEC, NUNCA LA FORMA SHELL. Con ["/app/server"] el binario es PID 1 y
# recibe el SIGTERM de `docker stop` directamente. Con la forma shell lo
# intercepta /bin/sh, que no lo retransmite, y el contenedor muere de un SIGKILL
# a los 10 segundos: las conexiones SSE se cortan de golpe y el WAL de SQLite
# queda sin cerrar. Verificado: por esta misma razón `go run` tampoco sirve como
# proceso principal.
ENTRYPOINT ["/app/server"]
