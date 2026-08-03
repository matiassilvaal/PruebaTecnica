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
mapfile -t SEGMENTOS < <(grep -v '^#' "$ORIGEN/$MANIFIESTO" | grep '\.ts$' | tr -d '\r')

if [ "${#SEGMENTOS[@]}" -eq 0 ]; then
  echo "error: $MANIFIESTO no nombra ningún .ts" >&2
  exit 1
fi

# Se comprueba TODO antes de copiar nada: es preferible fallar con la lista
# completa de lo que falta que dejar el destino a medio llenar.
FALTAN=()
for s in "${SEGMENTOS[@]}"; do
  [ -f "$ORIGEN/$s" ] || FALTAN+=("$s")
done

if [ "${#FALTAN[@]}" -gt 0 ]; then
  echo "error: el manifiesto nombra ${#SEGMENTOS[@]} segmentos y faltan ${#FALTAN[@]}:" >&2
  printf '  %s\n' "${FALTAN[@]}" >&2
  exit 1
fi

mkdir -p "$DESTINO"
cp "$ORIGEN/$MANIFIESTO" "$DESTINO/"
for s in "${SEGMENTOS[@]}"; do
  cp "$ORIGEN/$s" "$DESTINO/"
done

TAMANO=$(du -sh "$DESTINO" | cut -f1)
echo "listo: ${#SEGMENTOS[@]} segmentos y el manifiesto en $DESTINO/ ($TAMANO)"
echo "siguiente: docker build -t zapping-live ."
