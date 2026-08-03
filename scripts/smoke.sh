#!/usr/bin/env bash
#
# Verificación de humo sobre la imagen ya construida: levanta el contenedor,
# comprueba lo que se puede comprobar por HTTP, y lo apaga midiendo cuánto tarda.
#
# Cubre la parte automatizable de la checklist de docs/05-docker-y-entrega.md.
# Lo que NO cubre —y hay que mirar en el navegador— queda listado al final:
# reproducción continua durante 12 minutos, el contador con dos pestañas, y que
# no salga ninguna petición a un dominio externo.

set -uo pipefail

# Git Bash reescribe los argumentos que parecen rutas Unix, y a Docker eso le
# rompe cosas como `-v volumen:/data`, que llegaría como `-v volumen:C:/Program
# Files/Git/data`. La conversión se desactiva SÓLO para docker.
#
# Deliberadamente NO se exporta MSYS_NO_PATHCONV para todo el script: el curl de
# Windows necesita esa misma conversión para entender `/dev/null` y la ruta del
# jar de cookies. Con la variable puesta globalmente, curl aborta la descarga
# tras las cabeceras —200 con 0 bytes— y nunca escribe el jar, con lo que este
# script reporta fallos de la aplicación que en realidad son suyos.
d() { MSYS_NO_PATHCONV=1 docker "$@"; }

IMAGEN="${IMAGEN:-zapping-live}"
CONTENEDOR="zapping-humo-$$"
PUERTO="${PUERTO:-8099}"
BASE="http://localhost:$PUERTO"
GALLETA="$(mktemp)"

fallos=0
total=0

# El total se cuenta acá y no se escribe a mano en ningún lado: cualquier número
# fijo en el README o en los docs envejece en cuanto se agrega una comprobación.
ok()    { total=$((total + 1)); printf '  \033[32mok\033[0m    %s\n' "$1"; }
falla() { total=$((total + 1)); fallos=$((fallos + 1)); printf '  \033[31mFALLA\033[0m %s\n' "$1"; }

limpiar() {
  d rm -f "$CONTENEDOR" >/dev/null 2>&1 || true
  rm -f "$GALLETA"
}
trap limpiar EXIT

# Un volumen propio por corrida: si reusara uno con datos, el registro fallaría
# por email duplicado y parecería un bug del servidor.
VOLUMEN="zapping-humo-vol-$$"

echo "levantando $IMAGEN en el puerto $PUERTO"
d run -d --name "$CONTENEDOR" -p "$PUERTO:8080" -v "$VOLUMEN:/data" "$IMAGEN" >/dev/null

# Se espera al healthcheck en vez de dormir un tiempo fijo: el arranque incluye
# migraciones y el parseo de 64 EXTINF, y cuánto tarda depende de la máquina.
echo "esperando a que responda"
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null; then break; fi
  sleep 1
done

echo
echo "-- lo que se comprueba por HTTP --"

codigo() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

[ "$(codigo "$BASE/healthz")" = "200" ] \
  && ok "/healthz responde 200" \
  || falla "/healthz no responde 200"

# El requisito 4 del enunciado: al stream sólo entran cuentas registradas.
# 401 y no 302, porque un redirect haría que hls.js parsee la página de login
# como playlist y reporte un error incomprensible.
[ "$(codigo "$BASE/live/stream.m3u8")" = "401" ] \
  && ok "el playlist sin sesión da 401" \
  || falla "el playlist sin sesión NO da 401"

[ "$(codigo "$BASE/live/segments/segment0.ts")" = "401" ] \
  && ok "los segmentos sin sesión dan 401" \
  || falla "los segmentos sin sesión NO dan 401"

[ "$(codigo "$BASE/live/events")" = "401" ] \
  && ok "el SSE sin sesión da 401" \
  || falla "el SSE sin sesión NO da 401"

[ "$(codigo "$BASE/player")" = "302" ] \
  && ok "/player sin sesión redirige al login" \
  || falla "/player sin sesión NO redirige"

for ruta in /login /register /static/app.css /static/vendor/hls.min.js; do
  [ "$(codigo "$BASE$ruta")" = "200" ] \
    && ok "$ruta responde 200" \
    || falla "$ruta no responde 200"
done

echo
echo "-- con una cuenta recién creada --"

correo="humo-$$@ejemplo.cl"
alta=$(curl -s -c "$GALLETA" -o /dev/null -w '%{http_code}' -X POST "$BASE/register" \
  -d "nombre=Prueba+de+Humo&email=$correo&contrasena=contrasena-larga")
[ "$alta" = "302" ] \
  && ok "el registro crea la cuenta y redirige" \
  || falla "el registro devolvió $alta, quería 302"

grep -q "zapping_session" "$GALLETA" \
  && ok "se emitió la cookie de sesión" \
  || falla "no se emitió la cookie de sesión"

playlist=$(curl -s -b "$GALLETA" "$BASE/live/stream.m3u8")
grep -q '^#EXTM3U' <<<"$playlist" \
  && ok "el playlist llega y es un m3u8" \
  || falla "el playlist no parece un m3u8"

# Las URI del .m3u8 son relativas: sólo resuelven si el playlist y los segmentos
# son rutas hermanas. Se toma la primera del playlist servido y se pide.
primero=$(grep -m1 '\.ts$' <<<"$playlist" | tr -d '\r')
if [ -n "$primero" ]; then
  seg=$(curl -s -b "$GALLETA" -o /dev/null -w '%{http_code} %{size_download}' "$BASE/live/$primero")
  read -r cod tam <<<"$seg"
  { [ "$cod" = "200" ] && [ "$tam" -gt 100000 ]; } \
    && ok "el segmento del playlist se sirve ($primero, $tam bytes)" \
    || falla "el segmento $primero devolvió $cod con $tam bytes"
else
  falla "el playlist no nombra ningún .ts"
fi

evento=$(curl -s -N -b "$GALLETA" --max-time 3 "$BASE/live/events" | head -1)
grep -q '"viewers"' <<<"$evento" \
  && ok "el SSE entrega un evento con el contador" \
  || falla "el SSE no entregó un evento reconocible"

# El motor avanza derivando la posición del reloj: la secuencia tiene que subir.
seq1=$(grep -m1 'MEDIA-SEQUENCE' <<<"$playlist" | tr -dc '0-9')
sleep 11
seq2=$(curl -s -b "$GALLETA" "$BASE/live/stream.m3u8" | grep -m1 'MEDIA-SEQUENCE' | tr -dc '0-9')
[ "${seq2:-0}" -gt "${seq1:-0}" ] \
  && ok "la ventana rota: MEDIA-SEQUENCE $seq1 -> $seq2" \
  || falla "MEDIA-SEQUENCE no avanzó ($seq1 -> $seq2)"

echo
echo "-- persistencia y apagado --"

# El apagado tiene que ser ordenado: con el ENTRYPOINT en forma exec el binario
# es PID 1 y recibe el SIGTERM. Si tardara los 10 s completos, la señal no está
# llegando y el contenedor estaría muriendo de un SIGKILL.
inicio=$(date +%s%N)
d stop "$CONTENEDOR" >/dev/null
ms=$(( ($(date +%s%N) - inicio) / 1000000 ))
[ "$ms" -lt 5000 ] \
  && ok "docker stop apagó ordenadamente en ${ms} ms" \
  || falla "docker stop tardó ${ms} ms: ¿la señal llega al proceso?"

d logs "$CONTENEDOR" 2>&1 | grep -q "apagado limpio" \
  && ok "el log confirma el apagado limpio" \
  || falla "no aparece 'apagado limpio' en el log"

# La cuenta tiene que sobrevivir al reinicio: es lo que el volumen compra.
d start "$CONTENEDOR" >/dev/null
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null && break
  sleep 1
done
relogin=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/login" \
  -d "email=$correo&contrasena=contrasena-larga")
[ "$relogin" = "302" ] \
  && ok "la cuenta sobrevive al reinicio del contenedor" \
  || falla "el login tras reiniciar devolvió $relogin, quería 302"

d rm -f "$CONTENEDOR" >/dev/null 2>&1
d volume rm "$VOLUMEN" >/dev/null 2>&1

echo
if [ "$fallos" -eq 0 ]; then
  printf '\033[32m%d/%d en verde.\033[0m\n' "$total" "$total"
else
  printf '\033[31m%d de %d comprobaciones fallaron.\033[0m\n' "$fallos" "$total"
fi

cat <<'MANUAL'

Falta comprobar a mano, en el navegador (no se puede desde acá):
  - Reproducción continua durante 12 minutos. Cubre la vuelta del ciclo
    (10,5 min) y el segment63.ts de 4,57 s, que es el caso que descarta la
    solución del ticker fijo.
  - Dos pestañas en /player: el contador marca 2 en ambas, y vuelve a 1 al
    cerrar una. La cuenta regresiva del panel NO debe saltar hacia atrás.
  - DevTools > Network: cero peticiones a dominios externos.
MANUAL

exit "$fallos"
