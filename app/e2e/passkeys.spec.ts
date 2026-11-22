// Passkeys through Chromium's virtual authenticator: the real WebAuthn
// ceremonies between the app, the browser and the server.
import type { BrowserContext, Page } from '@playwright/test';
import { expect, test } from './support/fixtures';
import { acceptDialogs, expectSynced } from './support/app';

const PASSKEYS_ON = { WEBAUTHN_ENABLED: 'true' };

/**
 * Gives the page a platform authenticator (like a fingerprint reader). With
 * `approve(false)` the user verification fails, which the browser reports to
 * the page exactly like a prompt the user closed.
 */
async function addAuthenticator(context: BrowserContext, page: Page) {
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true
    }
  });
  return {
    approve: (isUserVerified: boolean) =>
      cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified }),
    credentials: async () =>
      (await cdp.send('WebAuthn.getCredentials', { authenticatorId })).credentials.length
  };
}

const passkeysCard = (page: Page) =>
  page.locator('section').filter({ has: page.getByRole('heading', { name: 'Passkeys' }) });

/** From Settings → Account: signs out and waits for the sign-in page. */
async function signOut(page: Page) {
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await page
    .getByRole('group', { name: 'Sign out' })
    .getByRole('button', { name: 'Sign out', exact: true })
    .click();
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('session'))).toBeNull();
}

/** On the Settings page: adds a passkey named `name` and waits until it is listed. */
async function enrol(page: Page, name: string) {
  const card = passkeysCard(page);
  await card.getByLabel('Name of the new passkey').fill(name);
  await card.getByRole('button', { name: 'Add passkey' }).click();
  await expect(card.getByText('Passkey added.')).toBeVisible();
  await expect(card.getByRole('listitem').filter({ hasText: name })).toBeVisible();
}

test('a passkey is added in Settings and signs in; a closed prompt is not an error', async ({
  startServer,
  apiFor,
  device
}) => {
  const server = await startServer(PASSKEYS_ON);
  const account = await apiFor(server).signUp();
  const { context, page } = await device({ session: account, server });
  const authenticator = await addAuthenticator(context, page);
  await page.goto('/settings?tab=account');
  const card = passkeysCard(page);
  await expect(card.getByText('No passkeys yet.')).toBeVisible();

  await test.step('closing the prompt while adding leaves everything as it was', async () => {
    await authenticator.approve(false);
    const began = page.waitForResponse((r) => r.url().includes('/auth/webauthn/enroll/begin'));
    await card.getByLabel('Name of the new passkey').fill('Phone');
    await card.getByRole('button', { name: 'Add passkey' }).click();
    expect((await began).status()).toBe(200);
    await expect(card.getByRole('button', { name: 'Add passkey' })).toBeEnabled();
    await expect(card.getByRole('status')).toHaveCount(0);
    await expect(card.getByText('No passkeys yet.')).toBeVisible();
    expect(await authenticator.credentials()).toBe(0);
  });

  await test.step('add the passkey', async () => {
    await authenticator.approve(true);
    await enrol(page, 'Phone');
    expect(await authenticator.credentials()).toBe(1);
  });

  await test.step('sign out', () => signOut(page));

  const withPasskey = page.getByRole('button', { name: 'Sign in with a passkey' });
  await test.step('closing the prompt while signing in shows no error', async () => {
    await authenticator.approve(false);
    const began = page.waitForResponse((r) => r.url().includes('/auth/webauthn/login/begin'));
    await withPasskey.click();
    expect((await began).status()).toBe(200);
    await expect(withPasskey).toBeEnabled();
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect(page).toHaveURL(/\/login$/);
    expect(await page.evaluate(() => localStorage.getItem('session'))).toBeNull();
  });

  await test.step('sign in with the passkey', async () => {
    await authenticator.approve(true);
    const finished = page.waitForResponse((r) => r.url().includes('/auth/webauthn/login/finish'));
    await withPasskey.click();
    expect((await finished).status()).toBe(200);
    await expectSynced(page);
    // Signed in as the account the passkey belongs to.
    await page.getByRole('navigation').getByRole('link', { name: 'Settings' }).click();
    await page.getByRole('link', { name: 'Account', exact: true }).click();
    await expect(
      page
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Account', exact: true }) })
        .getByText(account.email)
    ).toBeVisible();
    await expect(
      passkeysCard(page).getByRole('listitem').filter({ hasText: 'Phone' })
    ).toContainText('Last used');
  });

  await test.step('a removed passkey no longer signs in', async () => {
    const asked = acceptDialogs(page);
    const card = passkeysCard(page);
    const removed = page.waitForResponse((r) =>
      r.url().endsWith('/auth/webauthn/credentials/delete')
    );
    await card
      .getByRole('listitem')
      .filter({ hasText: 'Phone' })
      .getByRole('button', { name: 'Remove' })
      .click();
    expect((await removed).ok()).toBe(true);
    expect(asked).toContain('Remove this passkey? You can no longer sign in with it.');
    await expect(card.getByText('No passkeys yet.')).toBeVisible();

    await signOut(page);
    // The device still holds the key; the server no longer accepts it.
    expect(await authenticator.credentials()).toBe(1);
    const finished = page.waitForResponse((r) => r.url().includes('/auth/webauthn/login/finish'));
    await withPasskey.click();
    expect((await finished).ok()).toBe(false);
    await expect(page.getByRole('alert')).toContainText('Signing in with the passkey did not work');
    expect(await page.evaluate(() => localStorage.getItem('session'))).toBeNull();
  });
});

test('an account without a password confirms its deletion with a passkey', async ({
  startServer,
  apiFor,
  device
}) => {
  const server = await startServer(PASSKEYS_ON);
  const api = apiFor(server);
  const account = await api.signUp();
  const { context, page } = await device({ session: account, server });
  const authenticator = await addAuthenticator(context, page);
  await page.goto('/settings?tab=account');
  await enrol(page, 'Phone');

  // Accounts without a password come from a sign-in provider, which these
  // tests do not run: the password is taken away in the database instead.
  server.sql(`UPDATE users SET password_hash = '' WHERE id = ?`, account.userId);
  await page.reload();

  const card = page
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: 'Delete account' }) });
  await card.getByRole('button', { name: 'Delete my account…' }).click();
  await expect(
    card.getByText('You will be asked to confirm with one of your passkeys.')
  ).toBeVisible();
  await expect(card.getByLabel('Your password, to confirm')).toHaveCount(0);
  const confirm = card.getByRole('button', { name: 'Delete my account permanently' });

  await test.step('closing the passkey prompt cancels without a word', async () => {
    await authenticator.approve(false);
    const began = page.waitForResponse((r) => r.url().includes('/auth/webauthn/reauth/begin'));
    await confirm.click();
    expect((await began).status()).toBe(200);
    await expect(confirm).toBeEnabled();
    await expect(card.getByRole('alert')).toHaveCount(0);
    expect(await api.as(account).status('/auth/me')).toBe(200);
  });

  await test.step('confirming with the passkey deletes the account', async () => {
    await authenticator.approve(true);
    const proof = page.waitForResponse((r) => r.url().includes('/auth/webauthn/reauth/finish'));
    const deleted = page.waitForResponse((r) => r.url().endsWith('/auth/account/delete'));
    await confirm.click();
    expect((await proof).status()).toBe(200);
    expect((await deleted).status()).toBe(200);
    await expect(page).toHaveURL(/\/login\?deleted=1/);
    await expect(page.getByText('Your account was deleted.')).toBeVisible();
    expect(await api.as(account).status('/auth/me')).toBe(401);
    // The passkey is gone with the account: it opens nothing any more.
    const finished = page.waitForResponse((r) => r.url().includes('/auth/webauthn/login/finish'));
    await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
    expect((await finished).status()).not.toBe(200);
    await expect(page.getByRole('alert')).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });
});
