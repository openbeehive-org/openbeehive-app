// Getting around without a mouse, and the navigation at phone width.
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

test('a skip link leads past the navigation, and an opened form takes the focus', async ({
  api,
  device
}) => {
  const account = await api.signUp();
  const as = api.as(account);
  const hive = await as.createHive(await as.createApiary('Orchard'), 'Hive 7');

  const { page } = await device({ session: account });
  await page.goto(`/hives/${hive}`);
  await expect(page.getByRole('heading', { name: 'Hive 7' })).toBeVisible();
  await expectSynced(page);

  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: 'Skip to content' });
  await expect(skip).toBeFocused();
  await expect(skip).toBeInViewport();
  await page.keyboard.press('Enter');
  await expect(page.locator('main')).toBeFocused();

  await page.getByRole('button', { name: 'Record visit' }).first().click();
  await expect(page.getByRole('heading', { name: 'Record visit' })).toBeFocused();
});

for (const locale of ['en-US', 'de-DE', 'fr-FR', 'es-ES', 'it-IT']) {
  test(`at 360 px no label of the bottom navigation is cut off (${locale})`, async ({
    api,
    device
  }) => {
    const account = await api.signUp();
    const { page } = await device({
      session: account,
      context: { locale, viewport: { width: 360, height: 740 } }
    });
    await page.goto('/');
    await expect(page.locator('html')).toHaveAttribute('lang', locale.slice(0, 2));
    // Measured with the app's own font: until it has loaded, a wider
    // fallback font can cut a label that fits.
    await page.evaluate(() => document.fonts.ready);
    const cut = () =>
      page.evaluate(() =>
        [...document.querySelectorAll<HTMLElement>('.tabbar .lbl-short')]
          .filter((l) => l.scrollWidth > l.clientWidth)
          .map((l) => l.textContent)
      );
    await expect.poll(cut).toEqual([]);
  });
}
