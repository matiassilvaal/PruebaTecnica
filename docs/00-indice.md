# Índice del desarrollo — Prueba técnica Zapping

Proyecto: servicio de livestreaming HLS en Go, con registro de usuarios y player web.
Enunciado original: [../Prueba.md](../Prueba.md)

## Cómo está organizado

Cada documento numerado es un **bloque construible e independiente**: define su alcance,
los archivos que toca, sus criterios de aceptación y sus tests. Se implementan en orden.

| # | Documento | Contenido | Estado |
| --- | --- | --- | --- |
| 01 | [01-diseno.md](01-diseno.md) | Spec completo: arquitectura, paquetes, flujo de datos, modelo, rutas | ✅ Acordado |
| 02 | [02-motor-hls.md](02-motor-hls.md) | Pool, tabla de duraciones, snapshot inmutable, motor del reloj | ✅ Completo |
| 03 | [03-auth-y-db.md](03-auth-y-db.md) | SQLite, usuarios, bcrypt, sesiones, middleware | ✅ Completo |
| 04 | [04-web-y-frontend.md](04-web-y-frontend.md) | Handlers, templates, player, panel SSE, glassmorfismo | ✅ Completo |
| 05 | [05-docker-y-entrega.md](05-docker-y-entrega.md) | Dockerfile, preparación de segmentos, ejecución | ⬜ Pendiente — ojo: el `COPY web/` del diseño original **ya no aplica** (assets embebidos con `go:embed`) |
| 06 | [06-decisiones.md](06-decisiones.md) | Justificaciones para el README y el correo de entrega | 🔄 Vivo |

## Orden de implementación y por qué

**02 va primero.** Es la única pieza con riesgo técnico real y es el núcleo de lo que la
prueba evalúa. Si algo se complica, conviene que se complique temprano. Auth (03) y
frontend (04) son trabajo conocido y de riesgo bajo. Docker (05) se arma al final, cuando
ya hay algo que empaquetar.

**06 es un documento vivo:** se va llenando durante todo el desarrollo, no al final.
De ahí sale el README y el correo de entrega, que según las instrucciones recibidas debe
justificar el tiempo invertido.

## Decisiones cerradas

Resumen de lo acordado en la fase de diseño. El detalle y la justificación de cada una
está en [06-decisiones.md](06-decisiones.md).

- **Alcance:** sólido y profesional, con una feature opcional (contador de espectadores
  en vivo + panel de estado del stream vía SSE).
- **Stack:** Go stdlib (`net/http` con routing nativo 1.22+), SQLite vía
  `modernc.org/sqlite` (Go puro, sin cgo), sesiones propias en DB, bcrypt.
- **Motor de live:** tabla de duraciones acumuladas + secuencia derivada del reloj
  monotónico + snapshot inmutable publicado con `atomic.Pointer`.
- **Pool:** los 64 segmentos provistos, con `EXT-X-DISCONTINUITY` en la vuelta del ciclo.
- **Frontend:** glassmorfismo sobre CSS propio, sin Bootstrap, sin `backdrop-filter`
  encima del `<video>`. hls.js vendorizado y **embebido en el binario** con `go:embed`,
  junto con las plantillas, el CSS y el JS: por eso viven bajo `internal/web/` y no en un
  `web/` de la raíz, y por eso el Dockerfile no necesita copiarlos.
- **Panel en vivo:** hub SSE con una goroutine dueña del conjunto de clientes (sin mutex) y
  dos puntos de backpressure que descartan en vez de bloquear.
- **Dependencias totales:** dos (`modernc.org/sqlite`, `golang.org/x/crypto`).

## Requisitos del enunciado y dónde se cumplen

| Requisito | Documento |
| --- | --- |
| 1. Docker con el aplicativo funcionando | 05 |
| 2. Tres páginas: crear cuenta, login, player | 04 |
| 3. DB para registro de usuarios | 03 |
| 4. Al player sólo usuarios registrados | 03 (middleware) + 04 (rutas) |
| A. Microservicio HLS live en Go | 02 |
| A. 3 segmentos (30s) por request | 02 |
| A. Rotación y `EXT-X-MEDIA-SEQUENCE` creciente | 02 |
| B. HTML + JS + hls.js | 04 |
| Criterio: orden de código | 01 (estructura de paquetes) |
| Criterio: async/sync | 02 (motor), 04 (hub SSE) |
| Criterio: estructura de datos | 02 (prefijos + búsqueda binaria) |
| Criterio: manejo de RAM | 02 (snapshot, streaming de `.ts`), 04 (backpressure SSE) |
| Opcional: feature adicional | 04 (contador + panel live) |
| Opcional: detalle en las vistas | 04 (glassmorfismo) |
