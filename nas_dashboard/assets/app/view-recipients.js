(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html } = root.htmPreact;
  FW.views = FW.views || {};

  // Empfänger (Spec §6.3), Adresse #/empfaenger bzw. #/empfaenger/<id>.
  // Vorerst nur der große Titel - Liste, Person und Hinzufügen folgen.
  FW.views.Recipients = function Recipients() {
    return html`<${FW.ui.Page} title="Empfänger"/>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
