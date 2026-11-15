// Anmeldung: Cookie gilt 12 Std (übersteht das Schließen der iPhone-App),
// wird bei jeder angemeldeten Anfrage erneuert und beim Abmelden gelöscht.
const NAME = 'boss925_dashboard';
const find = (res) => (res.headers.getSetCookie?.() ?? []).find((c) => c.startsWith(NAME + '='));
const attr = (c, n) => c.split(';').map((p) => p.trim()).find((p) => p.toLowerCase().startsWith(n.toLowerCase() + '='))?.split('=')[1];
const expiresIn = (c) => {
  const ma = attr(c, 'Max-Age');
  if (ma !== undefined) return Number(ma);
  const ex = attr(c, 'expires');
  return ex === undefined ? null : (Date.parse(ex) - Date.now()) / 1000;
};

export default async function ({ base, assert }) {
  const login = await fetch(`${base}/index.php`, {
    method: 'POST',
    redirect: 'manual',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ action: 'login', user: 'admin', password: 'test' }),
  });
  const c1 = find(login);
  assert(c1, 'Login setzt das Cookie');
  let left = expiresIn(c1);
  assert(left !== null && Math.abs(left - 43200) <= 60, 'Login-Cookie ~12 h, war ' + left);
  assert(/httponly/i.test(c1) && /samesite=strict/i.test(c1), 'HttpOnly + SameSite=Strict: ' + c1);
  const cookie = c1.split(';')[0];

  const st = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie } });
  const c2 = find(st);
  assert(c2, 'Status-Anfrage erneuert das Cookie');
  left = expiresIn(c2);
  assert(left !== null && Math.abs(left - 43200) <= 60, 'erneuertes Cookie ~12 h, war ' + left);
  assert(attr(c2, 'path') === attr(c1, 'path'), 'gleicher Pfad: ' + attr(c1, 'path') + ' / ' + attr(c2, 'path'));
  assert(/httponly/i.test(c2) && /samesite=strict/i.test(c2), 'Attribute erneuert: ' + c2);
  assert(/secure/i.test(c1) === /secure/i.test(c2), 'Secure gleich');
  const csrf = (await st.json()).csrf;

  // Ohne Anmeldung: 401 (die leere Sitzung bekommt ihr Cookie, ist aber nicht angemeldet)
  const anon = await fetch(`${base}/dashboard_api.php?action=status`);
  assert(anon.status === 401, 'anonym 401');

  const out = await fetch(`${base}/index.php`, {
    method: 'POST',
    redirect: 'manual',
    headers: { cookie, 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ action: 'logout', csrf }),
  });
  const c3 = find(out);
  assert(c3, 'Logout sendet Set-Cookie');
  left = expiresIn(c3);
  assert(left !== null && left < 0, 'Logout-Cookie abgelaufen: ' + c3);
  const after = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie } });
  assert(after.status === 401, 'nach Logout 401');
}
