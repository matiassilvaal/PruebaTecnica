# Prueba Técnica — Zapping

> Transcripción de `Prueba v3. (1).pdf` + información entregada por correo.

---

## 1. Enunciado de la prueba

La prueba consiste en hacer un proyecto en **Go lang**.

1. Deberás entregar un **docker** con el aplicativo funcionando.
2. Debes crear un sitio web con **3 páginas**:
   - **Crear Cuenta**: un formulario que solicite nombre, email y contraseña.
   - **Login**
   - **Player**
3. Debes usar una **DB** para el registro de usuarios.
4. Al **Player** solo deben poder ingresar usuarios **registrados**.

**El desafío consiste en desarrollar un servicio que genere un Livestreaming para poder ser reproducido por un usuario registrado.**

---

### A. Player Backend

- Crear un microservicio que entregue un **Live Streaming HLS** con una aplicación Go lang.
- Utilizar los segmentos de video provistos, que duran **10 segundos** cada uno.
- Se deben entregar **30s de video por request** al servicio (**3 segmentos**).
- Para simular que es un livestreaming, **cada 10 segundos** se debe eliminar el **último** segmento (primero de la lista) y agregar un segmento nuevo al final de la lista.
- La etiqueta `EXT-X-MEDIA-SEQUENCE` aumenta secuencialmente cuando se quita un segmento.

#### Ejemplo archivo m3u8 Live Streaming HLS

```m3u8
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:3
#EXTINF:6.000000,
segmento_3.ts
#EXTINF:6.000000,
segmento_4.ts
#EXTINF:6.000000,
segmento_5.ts
#EXTINF:6.000000,
Segmento_6.ts
```

- Link para descargar segmentos:
  https://drive.google.com/file/d/1exGq6BJ6r1lXezOanp88sWwxqcMbDntJ/view?usp=sharing

---

### B. Frontend (Player)

- Debe tener un HTML con Javascript y un player **HLS.js** o **Video.js** para reproducir el Livestreaming generado en NodeJS.
- Se puede utilizar Bootstrap y Jquery, javascript. Lo que quieras.

---

### Se observará

- Orden de código.
- Buen uso de funciones asíncronas y síncronas.
- Estructura de datos.
- Buen manejo de la memoria RAM.

### Opcionales

- Cualquier función adicional interesante/divertida que el desarrollador quiera agregar.
- Cualquier detalle entretenido en las vistas HTML serán bienvenidos :).

---

## 2. Información del correo

> Hola Matías, ¿cómo estás?
>
> Te escribo para seguir con la siguiente etapa del proceso 👏🏼👏🏼👏🏼
>
> Te envío una prueba técnica que no es la idea que sea complicada.
>
> Algunas aclaraciones sobre la prueba:
>
> - No hay tiempo límite de entrega, pero obvio que influye en la competencia con los otros postulantes
> - Si tienes cualquier duda sobre la prueba no dudes en preguntar y te puedo agendar una reunión con Nacho y Claudio para que te expliquen bien.
> - Tu tiempo de entrega tiene que ser justificado, si es que es más detallada puedes demorarte más tiempo y se entiende, y si breve porque no quisiste desarrollarlo completamente pero se entiende y está explicado esta perfecto también.
>
> Cuando estés lista con la prueba me la envias con copia a Nacho y Claudio y te voy contando como continua el proceso.
>
> Si tienes dudas, preguntanos nomás.
>
> Saludos y mucha suerte!
