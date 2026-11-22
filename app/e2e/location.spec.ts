// "Use my location" in the apiary form: it fills in the device's position, and
// it is left out when the server's Permissions-Policy forbids asking for it.
import type { Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const HERE = { latitude: 48.137154, longitude: 11.576124 };

async function openEditForm(page: Page, apiary: string) {
  await page.goto(`/apiaries/${apiary}`);
  await expect(page.getByRole('heading', { name: 'Orchard Meadow' })).toBeVisible();
  await expectSynced(page);
  await page.getByRole('button', { name: 'Edit apiary' }).click();
}

test('the location button fills in where the device is', async ({ api, device }) => {
  const account = await api.signUp();
  const apiary = await api.as(account).createApiary('Orchard Meadow');
  const { page } = await device({
    session: account,
    context: { geolocation: HERE, permissions: ['geolocation'] }
  });
  await openEditForm(page, apiary);
  await page.getByRole('button', { name: 'Use my location' }).click();
  await expect(page.getByLabel('Latitude')).toHaveValue(String(HERE.latitude));
  await expect(page.getByLabel('Longitude')).toHaveValue(String(HERE.longitude));
});

test('no location button when the server forbids asking for it', async ({ api, device }) => {
  const account = await api.signUp();
  const apiary = await api.as(account).createApiary('Orchard Meadow');
  const { page } = await device({ session: account });
  // What a proxy sending `Permissions-Policy: geolocation=()` does to the page.
  await page.route('**/*', async (route) => {
    if (route.request().resourceType() !== 'document') return route.fallback();
    const response = await route.fetch();
    await route.fulfill({
      response,
      headers: { ...response.headers(), 'permissions-policy': 'geolocation=()' }
    });
  });
  await openEditForm(page, apiary);
  await expect(page.getByLabel('Latitude')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Use my location' })).toHaveCount(0);
});
