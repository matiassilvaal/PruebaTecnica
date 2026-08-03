#!/usr/bin/env bash
#
# Construye la imagen para las dos arquitecturas que importan y exporta un .tar
# por cada una, listo para entregar.
#
# Por qué dos archivos y no uno multi-arquitectura: `docker load` sólo acepta el
# formato docker-archive, que lleva una sola plataforma. Un índice OCI con las
# dos lo entienden únicamente las versiones recientes de Docker, y la entrega no
# puede depender de qué versión tenga quien la reciba.
#
# arm64 va primero a propósito: es lo que corre nativo en un Mac con Apple
# Silicon, que es la máquina más probable del otro lado.

set -euo pipefail

# Git Bash reescribe los argumentos que parecen rutas Unix y a Docker eso le
# rompe cosas. Se desactiva sólo para docker.
d() { MSYS_NO_PATHCONV=1 docker "$@"; }

DESTINO="${DESTINO:-dist}"
IMAGEN="${IMAGEN:-zapping-live}"

if [ ! -f segments/segment.m3u8 ]; then
  echo "error: segments/ está vacío. Corré antes:" >&2
  echo "       ./scripts/prepare-segments.sh \"hls test\"" >&2
  exit 1
fi

mkdir -p "$DESTINO"

for arq in arm64 amd64; do
  echo
  echo "== construyendo linux/$arq =="
  # Se etiqueta igual en las dos: quien reciba carga UNA sola y corre
  # `docker run zapping-live` sin tener que acordarse de un sufijo.
  d buildx build --platform "linux/$arq" -t "$IMAGEN" --load .

  real=$(d image inspect "$IMAGEN" --format '{{.Os}}/{{.Architecture}}')
  if [ "$real" != "linux/$arq" ]; then
    echo "error: se pidió linux/$arq y la imagen quedó en $real" >&2
    exit 1
  fi

  echo "== exportando =="
  # La etiqueta va COMPLETA, con :latest. `docker save zapping-live` exportaría
  # todas las etiquetas del repositorio, y cualquier `zapping-live:loquesea` que
  # haya quedado dando vueltas de una prueba anterior viajaría dentro del .tar:
  # el archivo pesaría el doble y quien lo cargue se encontraría con dos
  # imágenes, una de ellas de la arquitectura equivocada.
  d save "$IMAGEN:latest" -o "$DESTINO/$IMAGEN-$arq.tar"

  dentro=$(tar -xOf "$DESTINO/$IMAGEN-$arq.tar" manifest.json | grep -o '"RepoTags"' | wc -l | tr -d ' ')
  if [ "$dentro" != "1" ]; then
    echo "error: el .tar de $arq contiene $dentro imágenes, se esperaba 1" >&2
    exit 1
  fi
  echo "   $DESTINO/$IMAGEN-$arq.tar  ($(du -h "$DESTINO/$IMAGEN-$arq.tar" | cut -f1 | tr -d ' '))"
done

# Las instrucciones viajan junto a los .tar: quien los reciba por Drive no tiene
# por qué abrir el repositorio para saber qué hacer con ellos.
cat > "$DESTINO/LEEME.txt" <<'TXT'
zapping-live — imagen Docker
============================

Hay dos archivos. Cargá SÓLO EL QUE CORRESPONDA a tu máquina.

  zapping-live-arm64.tar   Mac con Apple Silicon (M1, M2, M3, M4)
  zapping-live-amd64.tar   PC con Windows o Linux, y Mac con Intel

Si no estás seguro, en una terminal:

  uname -m      ->  arm64   usá el archivo arm64
                ->  x86_64  usá el archivo amd64

Cargar y levantar (reemplazá <archivo> por el que elegiste):

  docker load -i <archivo>
  docker run -p 8080:8080 -v zapping-data:/data zapping-live

Y abrí http://localhost:8080 — creá una cuenta y entrás al reproductor.

El volumen `zapping-data` hace que los usuarios registrados sobrevivan a un
`docker stop` / `docker start`. Sin él, cada recreación del contenedor empieza
con la base vacía.

Nota para Git Bash en Windows: hay que anteponer MSYS_NO_PATHCONV=1 al
`docker run`, o Git Bash reescribe la ruta del volumen. En PowerShell, CMD,
macOS y Linux no hace falta.

Comprobación rápida de que quedó bien:

  curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/healthz
      -> 200
  curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/live/stream.m3u8
      -> 401, porque al stream sólo entran cuentas registradas
TXT

echo
echo "listo. Contenido de $DESTINO/:"
ls -lh "$DESTINO" | tail -n +2 | awk '{print "  "$9"  "$5}'
