(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html } = root.htmPreact;
  FW.views = FW.views || {};

  // Einstellungen (Spec §6.5), Adressen #/einstellungen/<unterseite>….
  // Vorerst nur der große Titel - Liste und Unterseiten folgen.
  FW.views.Settings = function Settings() {
    return html`<${FW.ui.Page} title="Einstellungen"/>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
