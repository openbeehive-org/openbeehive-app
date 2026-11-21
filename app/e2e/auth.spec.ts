import { expect, test } from './support/fixtures';
import { expectSynced, submitSignIn, syncedChip } from './support/app';
import { ADMIN } from './support/server';

test('a wrong password is answered in the language of the app', async ({ api, device }) => {
  const account = await api.signUp();
  const { page } = await device({ session: account });

  // The language is chosen in Settings and remembered on the device.
  await page.goto('/settings');
  await page.getByRole('button', { name: 'Deutsch' }).click();
  await expect(page.getByRole('heading', { name: 'Einstellungen' })).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('lang', 'de');
  await page.getByRole('link', { name: 'Konto' }).click();
  await page.getByRole('button', { name: 'Abmelden', exact: true }).click();
  await page
    .getByRole('group', { name: 'Abmelden' })
    .getByRole('button', { name: 'Abmelden', exact: true })
    .click();

  await expect(page.getByRole('heading', { name: 'Anmelden' })).toBeVisible();
  await page.getByLabel('E-Mail').fill(account.email);
  await page.getByLabel('Passwort').fill('not-the-password');
  await page.getByRole('button', { name: 'Anmelden', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('E-Mail oder Passwort ist falsch.');
  await expect(page).toHaveURL(/\/login/);

  // The right password still signs in.
  await page.getByLabel('Passwort').fill(account.password);
  await page.getByRole('button', { name: 'Anmelden', exact: true }).click();
  await expect(page.getByRole('navigation').getByRole('link', { name: 'Übersicht' })).toBeVisible();
});

test('repeated wrong passwords end in the rate-limit message', async ({ api, device }) => {
  const account = await api.signUp();
  const { page } = await device();
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();

  // Ten failures per account are allowed; the attempt after that is refused unseen.
  let refusedAfter = 0;
  for (let attempt = 1; attempt <= 12 && !refusedAfter; attempt++) {
    const answer = page.waitForResponse((r) => r.url().endsWith('/auth/signin'));
    await submitSignIn(page, account.email, `wrong-password-${attempt}`);
    if ((await answer).status() === 429) refusedAfter = attempt;
    else await expect(page.getByRole('alert')).toHaveText('Wrong email or password.');
  }
  expect(refusedAfter).toBe(11);
  await expect(page.getByRole('alert')).toHaveText(
    'Too many attempts. Please wait a few minutes and try again.'
  );

  // While the account is blocked the right password does not get through either.
  await submitSignIn(page, account.email, account.password);
  await expect(page.getByRole('alert')).toHaveText(
    'Too many attempts. Please wait a few minutes and try again.'
  );
  await expect(page).toHaveURL(/\/login/);
});

test('a forgotten password is reset with a link from the instance admin', async ({
  api,
  device
}) => {
  const account = await api.signUp();

  // No mail server: the sign-in page says who can help.
  const user = await device();
  await user.page.goto('/login');
  await user.page.getByRole('link', { name: 'Forgot password?' }).click();
  await expect(
    user.page.getByText('This server does not send email. Ask its administrator for a reset link')
  ).toBeVisible();

  const admin = await device({
    session: { ...(await api.signIn(ADMIN.email, ADMIN.password)), email: ADMIN.email }
  });
  await admin.page.goto('/settings?tab=account');
  const tool = admin.page
    .locator('section')
    .filter({ has: admin.page.getByRole('heading', { name: 'Reset a user’s password' }) });
  await tool.getByLabel('Email address of the account').fill(account.email);
  await tool.getByRole('button', { name: 'Create reset link' }).click();
  await expect(tool.getByRole('status')).toContainText(`Reset link for ${account.email}`);
  const link = (await tool.locator('code').innerText()).trim();
  expect(link).toMatch(/\/reset-password\?token=[0-9a-f]{64}$/);

  await user.page.goto(link);
  await expect(user.page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
  // The token leaves the address bar (and the history) once the page has it.
  await expect(user.page).toHaveURL(/\/reset-password$/);
  await user.page.getByLabel('New password', { exact: true }).fill('a-brand-new-password');
  await user.page.getByLabel('Repeat the new password').fill('a-brand-new-password');
  await user.page.getByRole('button', { name: 'Save new password' }).click();
  await expect(user.page).toHaveURL(/\/login\?reset=1/);
  await expect(
    user.page.getByText('Password changed. Sign in with your new password.')
  ).toBeVisible();

  await submitSignIn(user.page, account.email, account.password);
  await expect(user.page.getByRole('alert')).toHaveText('Wrong email or password.');
  await submitSignIn(user.page, account.email, 'a-brand-new-password');
  await expectSynced(user.page);

  // The link worked once.
  const again = await device();
  await again.page.goto(link);
  await again.page.getByLabel('New password', { exact: true }).fill('yet-another-password');
  await again.page.getByLabel('Repeat the new password').fill('yet-another-password');
  await again.page.getByRole('button', { name: 'Save new password' }).click();
  await expect(again.page.getByRole('alert')).toContainText(
    'This reset link is not valid any more'
  );
});

test('a member removed from the workspace they work in continues in another one', async ({
  api,
  device
}) => {
  const owner = await api.signUp();
  const member = await api.signUp();
  const club = await api.as(owner).createWorkspace('Bee Club');
  const invite = await api.as(club).invite(club.orgId, member.email);
  const inClub = await api.as(member).acceptInvite(invite);
  await api.as(club).createApiary('Club apiary');
  await api.as(member).createApiary('My own apiary');

  const { page } = await device({ session: { ...inClub, email: member.email } });
  await page.goto('/apiaries');
  await expect(page.getByRole('link', { name: /Club apiary/ })).toBeVisible();
  await expectSynced(page);

  await api.as(club).removeMember(club.orgId, member.userId);
  // The app notices with its next sync; tapping the chip starts one now.
  await syncedChip(page).click();

  await expect(page.getByRole('alert')).toHaveText(
    'You are no longer a member of the workspace you were working in. You are now in another of your workspaces.'
  );
  await expect(page).toHaveURL(/\/apiaries$/);
  await expect(page.getByRole('link', { name: /My own apiary/ })).toBeVisible();
  await expect(page.getByRole('link', { name: /Club apiary/ })).toHaveCount(0);
  expect(await page.evaluate(() => localStorage.getItem('obh.orgId'))).toBe(member.orgId);
});

test('an account cannot be deleted while it is the only admin of a shared workspace; a member can leave for good', async ({
  api,
  device
}) => {
  const owner = await api.signUp();
  const member = await api.signUp();
  // The owner's personal workspace becomes a shared one.
  const invite = await api.as(owner).invite(owner.orgId, member.email);
  await api.as(member).acceptInvite(invite);

  const deleteCard = (page: (typeof ownerDevice)['page']) =>
    page.locator('section').filter({ has: page.getByRole('heading', { name: 'Delete account' }) });

  const ownerDevice = await device({ session: owner });
  await ownerDevice.page.goto('/settings?tab=account');
  const blocked = deleteCard(ownerDevice.page);
  await expect(blocked).toContainText(
    'Not possible yet: you are the only admin of a workspace that has other members.'
  );
  await expect(blocked).toContainText('1 other member');
  await expect(blocked.getByRole('button', { name: /Delete my account/ })).toHaveCount(0);
  // The server refuses as well, whatever the page offers.
  expect(await api.as(owner).status('/auth/account/delete', { password: owner.password })).toBe(
    409
  );

  const memberDevice = await device({ session: member });
  await memberDevice.page.goto('/settings?tab=account');
  const card = deleteCard(memberDevice.page);
  await expect(card).toContainText('Deleted with all apiaries, hives, records and photos');
  await expect(card).toContainText('Kept for the other members');
  await card.getByRole('button', { name: 'Delete my account…' }).click();
  await card.getByLabel('Your password, to confirm').fill('not-the-password');
  await card.getByRole('button', { name: 'Delete my account permanently' }).click();
  await expect(card.getByRole('alert')).toHaveText('The password is not correct.');
  await card.getByLabel('Your password, to confirm').fill(member.password);
  await card.getByRole('button', { name: 'Delete my account permanently' }).click();

  await expect(memberDevice.page).toHaveURL(/\/login\?deleted=1/);
  await expect(memberDevice.page.getByText('Your account was deleted.')).toBeVisible();
  await submitSignIn(memberDevice.page, member.email, member.password);
  await expect(memberDevice.page.getByRole('alert')).toHaveText('Wrong email or password.');

  // With the member gone nothing blocks the owner any more.
  await ownerDevice.page.reload();
  await expect(
    deleteCard(ownerDevice.page).getByRole('button', { name: 'Delete my account…' })
  ).toBeVisible();
});

test('an invite link leaves the address bar once read, and still works', async ({
  api,
  device
}) => {
  const owner = await api.signUp();
  const email = `${Date.now()}-invited@e2e.test`;
  const invite = await api.as(owner).invite(owner.orgId, email);

  const { page } = await device();
  await page.goto(`/login?invite=${invite}`);
  await expect(page.getByText(/been invited/)).toBeVisible();
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill('e2e-password-1');
  await page.getByRole('button', { name: 'Create account' }).click();
  await expectSynced(page);
  const me = await page.evaluate(() => localStorage.getItem('obh.orgId'));
  expect(me).toBe(owner.orgId);
});
