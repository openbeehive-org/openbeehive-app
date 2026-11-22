// The footer names the build of the app and links to the docs, the source and
// the project's homepage, on app pages and on the sign-in page.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const SOURCE = 'https://github.com/openbeehive-org/openbeehive-app';
const version: string = JSON.parse(
  fs.readFileSync(new URL('../package.json', import.meta.url), 'utf8')
).version;
// `make e2e` builds the app from this checkout, so its commit is git's HEAD.
const commit = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();

test('app pages end with the build and the project links', async ({ api, device }) => {
  const account = await api.signUp();
  const { page } = await device({ session: account });
  await page.goto('/');
  await expectSynced(page);

  const footer = page.locator('footer.app-footer');
  await footer.scrollIntoViewIfNeeded();
  await expect(footer).toContainText('Openbeehive');
  await expect(footer.getByRole('link', { name: `Version ${version}` })).toHaveAttribute(
    'href',
    `${SOURCE}/releases/tag/v${version}`
  );
  await expect(footer.getByRole('link', { name: `Build ${commit.slice(0, 7)}` })).toHaveAttribute(
    'href',
    `${SOURCE}/commit/${commit}`
  );
  await expect(footer).toContainText(/built .*\d{4}/);

  const links = footer.getByRole('navigation', { name: 'About this app' });
  await expect(links.getByRole('link', { name: 'Documentation' })).toHaveAttribute(
    'href',
    'https://docs.openbeehive.org'
  );
  await expect(links.getByRole('link', { name: 'Source code' })).toHaveAttribute('href', SOURCE);
  await expect(links.getByRole('link', { name: 'openbeehive.org' })).toHaveAttribute(
    'href',
    'https://openbeehive.org'
  );
  // External links open in a new tab and keep this page out of their reach.
  for (const link of await footer.getByRole('link').all()) {
    await expect(link).toHaveAttribute('target', '_blank');
    await expect(link).toHaveAttribute('rel', /noopener/);
  }
});

test('the sign-in page shows the footer in the UI language', async ({ device }) => {
  const { page } = await device({ context: { locale: 'de-DE' } });
  await page.goto('/login');
  const footer = page.locator('footer.app-footer');
  await expect(footer.getByRole('link', { name: `Version ${version}` })).toBeVisible();
  await expect(footer.getByRole('link', { name: 'Dokumentation' })).toBeVisible();
  await expect(footer.getByRole('link', { name: 'Quellcode' })).toBeVisible();
  await expect(footer).toContainText('erstellt am');
});
