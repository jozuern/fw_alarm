(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html } = root.htmPreact;
  FW.views = FW.views || {};

  // Übersicht (Spec §6.2). Vorerst nur der große Titel - Status-Karte,
  // Prüfliste und Auslösen folgen.
  FW.views.Overview = function Overview() {
    return html`<${FW.ui.Page} title="Übersicht"/>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
