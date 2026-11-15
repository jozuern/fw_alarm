// Dashboard-API: Dauer bis "automatisch wieder laut" (settings_list / settings_set).
import { apiSession } from '../cdp.mjs';

export default async function ({ base, assert }) {
  const s = await apiSession(base, 'admin', 'test');
  let r = await s.get('settings_list');
  assert(r.settings.mute_max_seconds.value === 43200, 'Standard 43200, war ' + r.settings.mute_max_seconds.value);
  assert(r.settings.mute_max_seconds.choices.some((c) => c.value === 7200), 'Stufe 7200 fehlt');
  r = await s.post({ action: 'settings_set', key: 'mute_max_seconds', value: '14400' });
  assert(r.ok === true, 'settings_set 14400: ' + JSON.stringify(r));
  assert((await s.get('keys_list')).mute_max_seconds === 14400, 'keys_list zeigt 14400');
  r = await s.post({ action: 'settings_set', key: 'mute_max_seconds', value: '999' });
  assert(r.ok === false, 'Wert 999 muss abgelehnt werden');
  assert((await s.get('keys_list')).mute_max_seconds === 14400, 'Wert bleibt 14400');
  r = await s.post({ action: 'settings_set', key: 'foo', value: '7200' });
  assert(r.ok === false, 'unbekannter Schlüssel abgelehnt');

  const ro = await apiSession(base, 'leser', 'test');
  assert((await ro.post({ action: 'settings_set', key: 'mute_max_seconds', value: '7200' })).status === 403, 'Lese-Rolle 403');
  assert((await ro.get('settings_list')).status === 403, 'Lese-Rolle settings_list 403');
  const log = await s.get('log_view', { limit: 5, filter: 'system' });
  assert(log.entries[0].event === 'setting_changed', 'Verlauf: ' + log.entries[0]?.event);

  // Aufräumen: zurück auf den Standard
  r = await s.post({ action: 'settings_set', key: 'mute_max_seconds', value: '43200' });
  assert(r.ok === true, 'Zurücksetzen');
}
