(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html } = root.htmPreact;
  FW.views = FW.views || {};

  // Verlauf (Spec §6.4), Adresse #/verlauf bzw. #/verlauf?filter=alarm.
  // Vorerst nur der große Titel - Filter, Suche und Einträge folgen.
  FW.views.Log = function Log() {
    return html`<${FW.ui.Page} title="Verlauf"/>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
