#!/usr/bin/env bash
#
# Copia el manifiesto y los segmentos .ts desde la carpeta provista a ./segments/,
# que es lo que el Dockerfile mete en la imagen.
#
# La verificación se hace CONTRA EL MANIFIESTO y no contra un número fijo: se
# comprueba que exista cada .ts que el .m3u8 nombra. Un conteo hardcodeado daría
# por buena una copia a la que le falta justo el archivo que el manifiesto pide,
# y el error aparecería recién al arrancar el contenedor.

set -euo pipefail

ORIGEN="${1:-}"
DESTINO="segments"
MANIFIESTO="segment.m3u8"

if [ -z "$ORIGEN" ]; then
  cat >&2 <<'USO'
uso: scripts/prepare-segments.sh <carpeta-con-los-segmentos>

  La carpeta debe contener segment.m3u8 y los .ts que ese manifiesto nombra.
  Ejemplo:  scripts/prepare-segments.sh "hls test"
USO
  exit 2
fi

if [ ! -d "$ORIGEN" ]; then
  echo "error: no existe la carpeta $ORIGEN" >&2
  exit 1
fi

if [ ! -f "$ORIGEN/$MANIFIESTO" ]; then
  echo "error: no se encontró $ORIGEN/$MANIFIESTO" >&2
  echo "       ¿es la carpeta correcta? debe traer el manifiesto junto a los .ts" >&2
  exit 1
fi

# Los nombres de segmento son las líneas del manifiesto que no son etiquetas.
#
# Se leen a un archivo temporal y no a un array con `mapfile`: eso es bash 4+, y
# macOS todavía trae bash 3.2. Este script tiene que correr tal cual en la
# máquina de quien evalúe, sea la que sea.
LISTA=$(mktemp)
trap 'rm -f "$LISTA" "$LISTA.faltan"' EXIT

grep -v '^#' "$ORIGEN/$MANIFIESTO" | grep '\.ts$' | tr -d '\r' > "$LISTA"

CUANTOS=$(wc -l < "$LISTA" | tr -d ' ')
if [ "$CUANTOS" -eq 0 ]; then
  echo "error: $MANIFIESTO no nombra ningún .ts" >&2
  exit 1
fi

# Se comprueba TODO antes de copiar nada: es preferible fallar con la lista
# completa de lo que falta que dejar el destino a medio llenar.
: > "$LISTA.faltan"
while IFS= read -r s; do
  [ -f "$ORIGEN/$s" ] || echo "$s" >> "$LISTA.faltan"
done < "$LISTA"

FALTAN=$(wc -l < "$LISTA.faltan" | tr -d ' ')
if [ "$FALTAN" -gt 0 ]; then
  echo "error: el manifiesto nombra $CUANTOS segmentos y faltan $FALTAN:" >&2
  sed 's/^/  /' "$LISTA.faltan" >&2
  exit 1
fi

mkdir -p "$DESTINO"
cp "$ORIGEN/$MANIFIESTO" "$DESTINO/"
while IFS= read -r s; do
  cp "$ORIGEN/$s" "$DESTINO/"
done < "$LISTA"

TAMANO=$(du -sh "$DESTINO" | cut -f1 | tr -d ' ')
echo "listo: $CUANTOS segmentos y el manifiesto en $DESTINO/ ($TAMANO)"
echo "siguiente: docker build -t zapping-live ."
