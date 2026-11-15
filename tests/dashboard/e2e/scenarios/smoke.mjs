// Rauchtest: Admin-Login, Seitentitel, Screenshot.
export default async function ({ browser, base, assert, shot }) {
  const page = await browser.newPage();
  await page.viewport({ width: 1280, height: 900 });
  await page.login(base, 'admin', 'test');
  await page.waitFor(`document.title.includes('Alarm-Wächter')`);
  const title = await page.eval('document.title');
  assert(title.includes('Alarm-Wächter'), `Titel enthält Alarm-Wächter (war: ${title})`);
  // Dem ersten Status-Poll Zeit geben, damit der Screenshot Inhalt zeigt.
  await new Promise((r) => setTimeout(r, 1500));
  await shot(page, 'admin');
}
