// Player del livestream y panel de estado.
//
// Dos fuentes de datos independientes: hls.js consume el .m3u8 por HTTP, y un
// EventSource recibe el estado del stream por SSE. El panel se monta dentro de
// un try/catch para que ningún problema suyo pueda tumbar la reproducción: si
// el panel falla, el video sigue.
(function () {
  'use strict';

  var video = document.getElementById('video');
  var fallo = document.getElementById('fallo');
  var volver = document.getElementById('volver-al-vivo');

  function mostrarFallo(texto) {
    if (!fallo) return;
    fallo.textContent = texto;
    fallo.hidden = false;
  }

  // Las URL llegan desde el HTML que renderiza el servidor, que es quien conoce
  // su propio árbol de rutas. Repetirlas acá crearía una segunda fuente de
  // verdad que puede divergir de la primera sin que nada se queje.
  //
  // Sin este elemento no hay URL de playlist, así que tampoco hay nada que
  // reproducir: se falla acá, con un mensaje, en vez de lanzar una excepción
  // que dejaría la página muda.
  var escenario = document.querySelector('.escenario');
  if (!escenario || !escenario.dataset.playlist) {
    mostrarFallo('No se pudo determinar la URL de la transmisión.');
    return;
  }
  var urlPlaylist = escenario.dataset.playlist;
  var urlEventos = escenario.dataset.eventos;

  // ---------- video ----------

  if (window.Hls && window.Hls.isSupported()) {
    var hls = new Hls({
      // Con una ventana de 3 segmentos —el mínimo que permite la spec de
      // HLS— esto posiciona al player al COMIENZO de la ventana, que es la
      // posición con más colchón disponible antes de quedarse sin datos.
      liveSyncDurationCount: 3,
      lowLatencyMode: false, // no aplica: esto no es LL-HLS
      enableWorker: true,    // el demux sale del hilo principal
    });

    hls.on(Hls.Events.ERROR, function (_evento, datos) {
      if (!datos.fatal) return;

      // Un 401 significa que la sesión venció mientras la pestaña estaba
      // abierta. Reintentar sería un bucle: hay que volver a entrar.
      var respuesta = datos.response;
      if (respuesta && respuesta.code === 401) {
        window.location.href = '/login';
        return;
      }

      switch (datos.type) {
        case Hls.ErrorTypes.NETWORK_ERROR:
          mostrarFallo('Problema de red. Reintentando…');
          hls.startLoad();
          break;
        case Hls.ErrorTypes.MEDIA_ERROR:
          // Típicamente la vuelta del ciclo: los timestamps saltan hacia
          // atrás y el decodificador necesita reinicializarse.
          mostrarFallo('Recuperando el video…');
          hls.recoverMediaError();
          break;
        default:
          mostrarFallo('No se pudo reproducir la transmisión.');
          hls.destroy();
      }
    });

    // Con la misma guarda que mostrarFallo: si la plantilla no trae el cartel,
    // esto no debe lanzar dentro de un manejador de hls.js, que corre por cada
    // fragmento y dejaría la consola llena de excepciones.
    hls.on(Hls.Events.FRAG_BUFFERED, function () {
      if (fallo) fallo.hidden = true;
    });

    hls.loadSource(urlPlaylist);
    hls.attachMedia(video);
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    // Safari reproduce HLS de forma nativa y no necesita hls.js.
    video.src = urlPlaylist;
  } else {
    mostrarFallo('Este navegador no puede reproducir HLS.');
  }

  // "Volver al vivo": si el usuario pausa, queda atrás respecto del borde en
  // vivo. Se le ofrece el salto en vez de dejarlo mirando el pasado sin saberlo.
  video.addEventListener('timeupdate', function () {
    if (!video.seekable || video.seekable.length === 0) return;
    var borde = video.seekable.end(video.seekable.length - 1);
    volver.hidden = (borde - video.currentTime) < 12;
  });
  volver.addEventListener('click', function () {
    if (video.seekable && video.seekable.length > 0) {
      video.currentTime = video.seekable.end(video.seekable.length - 1);
    }
    video.play();
    volver.hidden = true;
  });

  // ---------- panel ----------
  //
  // Todo el panel va dentro de un try/catch. Es la única forma de sostener de
  // verdad la promesa de la cabecera: el video ya está inicializado a esta
  // altura, y una excepción acá —un id que alguien renombró en la plantilla,
  // un EventSource que el navegador rechaza— abortaría el resto del script sin
  // este aislamiento, dejando también el video sin sus manejadores.
  try {
  var elEspectadores = document.getElementById('espectadores');
  var elSecuencia = document.getElementById('secuencia');
  var elVentana = document.getElementById('ventana');
  var elCuenta = document.getElementById('cuenta-regresiva');
  var elProgreso = document.getElementById('progreso');
  var elDisc = document.getElementById('discontinuidad');

  // Referencia de la cuenta regresiva. El servidor manda nextRotationMs una
  // vez por rotación; entre evento y evento interpolamos con el reloj local.
  // Pedirle al servidor la fracción que falta sería una petición por frame.
  var vencimiento = 0;
  var duracionTramo = 1;
  var ultimaSecuencia = null;

  var fuente = new EventSource(urlEventos);

  fuente.onmessage = function (mensaje) {
    var e;
    try {
      e = JSON.parse(mensaje.data);
    } catch (_) {
      return; // un evento ilegible no debe romper el panel
    }

    elEspectadores.textContent = e.viewers;
    elSecuencia.textContent = e.sequence;
    elDisc.hidden = !e.discontinuity;

    if (e.sequence !== ultimaSecuencia) {
      pintarVentana(e.window || []);
      ultimaSecuencia = e.sequence;
    }

    var falta = Math.max(0, e.nextRotationMs || 0);
    vencimiento = performance.now() + falta;
    duracionTramo = Math.max(falta, 1);
  };

  fuente.onerror = function () {
    // EventSource reconecta solo; sólo lo reflejamos en el panel. Un 401 tras
    // vencer la sesión termina en readyState CLOSED, y ahí sí hay que volver
    // a entrar.
    if (fuente.readyState === EventSource.CLOSED) {
      window.location.href = '/login';
    }
  };

  function pintarVentana(nombres) {
    elVentana.replaceChildren();
    nombres.forEach(function (nombre, i) {
      var li = document.createElement('li');
      // "segment14.ts" → "14": el panel es angosto y el prefijo es ruido.
      li.textContent = nombre.replace(/^segment/, '').replace(/\.ts$/, '');
      li.title = nombre;
      if (i === nombres.length - 1) li.classList.add('entrando');
      elVentana.appendChild(li);
    });
    // Quitar la clase en el siguiente frame dispara la transición del CSS.
    requestAnimationFrame(function () {
      var ultimo = elVentana.lastElementChild;
      if (ultimo) ultimo.classList.remove('entrando');
    });
  }

  function animar(ahora) {
    // Hasta que llegue el primer evento no hay referencia que interpolar:
    // pintar antes mostraría "0.0 s" y la barra llena durante un instante,
    // como si la rotación estuviera vencida.
    if (vencimiento > 0) {
      var restante = Math.max(0, vencimiento - ahora);
      elCuenta.textContent = (restante / 1000).toFixed(1) + ' s';
      elProgreso.style.width = (100 - (restante / duracionTramo) * 100).toFixed(1) + '%';
    }
    requestAnimationFrame(animar);
  }
  requestAnimationFrame(animar);

  } catch (err) {
    // El panel es la feature opcional; el video es el requisito. Si el panel
    // no se pudo montar, se pierde el panel y nada más.
    if (window.console) console.error('no se pudo montar el panel:', err);
  }
})();
