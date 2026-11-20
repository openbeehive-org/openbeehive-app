// Things a test does in the app again and again.
import { expect, type Page } from '@playwright/test';

/** The status chip above the content once everything is uploaded and pulled. */
export const syncedChip = (page: Page) => page.getByRole('button', { name: /^Synced/ });

/** Waits until this device has nothing left to upload and has pulled. */
export async function expectSynced(page: Page): Promise<void> {
  await expect(syncedChip(page)).toBeVisible();
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
