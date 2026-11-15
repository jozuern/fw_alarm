// Design vor dem ersten Zeichnen setzen: Wer „Hell“ gewählt hat, sähe sonst
// beim Laden kurz das dunkle Standard-Design, bis main.js läuft (bzw. auf der
// Anmeldeseite, die main.js gar nicht lädt, nie etwas anderes). Wird synchron
// im <head> geladen, vor dem Stylesheet; eigene Datei, weil die CSP keine
// Inline-Skripte erlaubt. Gleiche Werte wie FW.main (Schlüssel fwAlarm.theme,
// dark | light | system, Standard dark). Ohne Speicher/Fehler: Dunkel.
(function () {
  'use strict';
  try {
    var theme = 'dark';
    try {
      var stored = window.localStorage.getItem('fwAlarm.theme');
      if (stored === 'light' || stored === 'system') theme = stored;
    } catch (err) { /* privater Modus o.ä.: Standard */ }
    document.documentElement.setAttribute('data-theme', theme);
    // Farbe der Browser-/Statusleiste passend zum Hintergrund.
    var light = theme === 'light'
      || (theme === 'system' && !!window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches);
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', light ? '#f2f2f7' : '#000000');
  } catch (err) { /* nie den Seitenaufbau stören */ }
})();
