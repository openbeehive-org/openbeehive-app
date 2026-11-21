// Things a test does in the app again and again.
import { expect, type Page } from '@playwright/test';

/** The status chip above the content once everything is uploaded and pulled. */
export const syncedChip = (page: Page) => page.getByRole('button', { name: /^Synced/ });

/**
 * Waits until this device has nothing left to upload and has pulled. A first
 * sync downloads the whole workspace (the demo has many records), which
 * takes several seconds on a busy machine, so this waits longer than the
 * default 10 s; it still returns as soon as the chip says so.
 */
export async function expectSynced(page: Page, timeout = 30_000): Promise<void> {
  await expect(syncedChip(page)).toBeVisible({ timeout });
}

/**
 * Opens the sheet behind the status chip (whatever it says) and starts a
 * sync from there; the sheet closes itself.
 */
export async function syncNowFromChip(page: Page): Promise<void> {
  await page.locator('button[aria-controls="sync-sheet"]').click();
  await page
    .getByRole('dialog', { name: 'Sync status' })
    .getByRole('button', { name: 'Sync now' })
    .click();
}

/**
 * Accepts the app's confirm() questions and returns the list they are
 * collected in, so a test can assert what was asked.
 */
export function acceptDialogs(page: Page): string[] {
  const asked: string[] = [];
  page.on('dialog', (dialog) => {
    asked.push(dialog.message());
    void dialog.accept();
  });
  return asked;
}

/** Fills the sign-in form and submits it; the caller asserts the outcome. */
export async function submitSignIn(page: Page, email: string, password: string): Promise<void> {
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
}

/**
 * Waits until the service worker has cached the app and controls the page:
 * from then on the app opens without a network.
 */
export async function expectInstalled(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller) {
      await new Promise((resolve) =>
        navigator.serviceWorker.addEventListener('controllerchange', resolve, { once: true })
      );
    }
  });
}
