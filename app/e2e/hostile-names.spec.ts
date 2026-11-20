import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

// A name that runs script wherever it is inserted as markup instead of text.
const HOSTILE = '<img src=x onerror=window.__xss=1>';

// A transparent 1x1 PNG, served for every map tile.
const TILE = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
  'base64'
);

/** The name is on the page as plain text, and nothing it contains became an element or ran. */
async function expectShownAsText(page: Page, where: string) {
  await expect(page.getByText(HOSTILE, { exact: true }).first(), where).toBeAttached();
  await expect(page.locator('img[src="x"]'), where).toHaveCount(0);
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss), where).toBe(
    undefined
  );
}

test('names that look like markup stay text in the map, the QR views and the treatment register', async ({
  startServer,
  apiFor,
  device
}) => {
  // Maps on, with tiles from the test server's own address (answered below).
  const server = await startServer((url) => ({
    MAPS: 'on',
    MAP_TILE_URL: `${url}/e2e-tiles/{z}/{x}/{y}.png`,
    GEOCODER_URL: `${url}/e2e-geocoder`
  }));
  const api = apiFor(server);
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary(HOSTILE, { lat: 50.1, lng: 8.6 });
  const hive = await as.createHive(apiary, HOSTILE);
  await as.createTreatment(hive, HOSTILE, { dose: HOSTILE, method: HOSTILE });

  const { context, page, problems } = await device({ session: account, server });
  let tiles = 0;
  await context.route('**/e2e-tiles/**', (route) => {
    tiles += 1;
    return route.fulfill({ contentType: 'image/png', body: TILE });
  });
  // Printing opens a dialog nobody answers here.
  await context.addInitScript(() => {
    window.print = () => {};
  });

  await test.step('map on the apiaries page', async () => {
    await page.goto('/apiaries');
    await expect(page.getByRole('link', { name: HOSTILE })).toBeVisible();
    const marker = page.getByTitle(HOSTILE, { exact: true });
    await expect(marker).toBeVisible();
    await expect.poll(() => tiles).toBeGreaterThan(0);
    await expectShownAsText(page, 'apiaries');
    // Opens the pin's popup and, like a tap, goes on to the apiary.
    await marker.click();
    await expect(page).toHaveURL(new RegExp(`/apiaries/${apiary}$`));
    await expect(page.getByRole('heading', { name: HOSTILE, level: 1 })).toBeVisible();
    await expectShownAsText(page, 'after the map popup');
  });

  await test.step('QR sheet of the apiary', async () => {
    await page.emulateMedia({ media: 'print' });
    await page.getByRole('button', { name: 'QR labels' }).click();
    const sheet = page.locator('.print-sheet');
    await expect(sheet.getByRole('heading', { name: HOSTILE, includeHidden: true })).toBeVisible();
    await expect(sheet.getByText(HOSTILE, { exact: true })).toHaveCount(2);
    await expect(sheet.locator('img')).toHaveCount(0);
    await expectShownAsText(page, 'QR sheet');
    await page.emulateMedia({ media: 'screen' });
  });

  await test.step('treatment register of the apiary', async () => {
    const opened = context.waitForEvent('page');
    await page.getByRole('button', { name: 'Treatment register' }).click();
    const register = await opened;
    await expect(register.getByRole('heading', { name: 'Treatment register' })).toBeVisible();
    await expect(register.getByRole('heading', { name: `${HOSTILE} · ${HOSTILE}` })).toBeVisible();
    await expect(register.getByRole('cell', { name: HOSTILE, exact: true })).toHaveCount(3);
    await expect(register.locator('img')).toHaveCount(0);
    await expectShownAsText(register, 'apiary register');
    await register.close();
  });

  await test.step('QR label and treatment register of the hive', async () => {
    await page.getByRole('link', { name: HOSTILE }).click();
    await expect(page).toHaveURL(new RegExp(`/hives/${hive}$`));
    await expect(page.getByRole('heading', { name: HOSTILE, level: 1 })).toBeVisible();
    await expectShownAsText(page, 'hive page');

    await page.getByRole('button', { name: 'More' }).click();
    await page.getByRole('button', { name: 'QR code' }).click();
    let opened = context.waitForEvent('page');
    await page.getByRole('button', { name: 'Print', exact: true }).click();
    const label = await opened;
    await expect(label.locator('.name')).toHaveText(HOSTILE);
    await expect(label.locator('img')).toHaveCount(0);
    await expectShownAsText(label, 'QR label');
    await label.close();

    opened = context.waitForEvent('page');
    await page.getByRole('button', { name: 'Print register' }).click();
    const register = await opened;
    await expect(register.getByRole('cell', { name: HOSTILE, exact: true })).toHaveCount(3);
    await expect(register.locator('img')).toHaveCount(0);
    await expectShownAsText(register, 'hive register');
    await register.close();
  });

  await expectSynced(page);
  expect(problems.csp).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBe(
    undefined
  );
});
