// Policy and accessibility smoke test over the main routes, in both themes.
import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import type { Api } from './support/api';
import { expect, test } from './support/fixtures';
import { expectSynced } from './support/app';

const PHONE = { width: 360, height: 740 };
const DAY_MS = 24 * 60 * 60 * 1000;

/** An account with enough records that every route has something to show. */
async function seed(api: Api) {
  const account = await api.signUp();
  const as = api.as(account);
  const apiary = await as.createApiary('Orchard Meadow', {
    address: 'Mill Lane 3',
    note: 'Behind the barn'
  });
  const hive = await as.createHive(apiary, 'Hive 7');
  for (const [i, frames] of [6, 8, 9].entries()) {
    await as.createInspection(hive, {
      date: new Date(Date.now() - (30 - i * 10) * DAY_MS).toISOString(),
      frames,
      broodFrames: frames - 3,
      queenCells: i,
      weightKg: 30 + i,
      varroa: String(i + 1),
      tempHive: 34,
      tempOutside: 18,
      stores: 2,
      temperament: 2,
      calmness: 3,
      queenSeen: true,
      note: `Visit number ${i + 1}`
    });
  }
  await as.createHarvest(hive, 12, { variety: 'spring blossom' });
  await as.createTreatment(hive, 'Oxuvar', { dose: '50 ml' });
  await as.rpc('TaskService/CreateTask', { title: 'Order frames', hiveId: hive });
  return { account, apiary, hive };
}

type Route = {
  name: string;
  path: string;
  /** Resolves when the route shows its content. */
  ready: (page: Page) => Promise<void>;
  /** Opened without a session. */
  signedOut?: boolean;
};

const heading = (name: string) => (page: Page) =>
  expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
const text = (content: string) => (page: Page) =>
  expect(page.getByText(content).first()).toBeVisible();

function routes(ids: { apiary: string; hive: string }): Route[] {
  return [
    { name: 'dashboard', path: '/', ready: heading('Welcome back') },
    { name: 'apiaries', path: '/apiaries', ready: text('Mill Lane 3') },
    { name: 'apiary', path: `/apiaries/${ids.apiary}`, ready: heading('Orchard Meadow') },
    { name: 'hives', path: '/hives', ready: text('Hive 7') },
    { name: 'hive', path: `/hives/${ids.hive}`, ready: heading('Hive 7') },
    { name: 'visits', path: `/hives/${ids.hive}/visits`, ready: text('Visit number 1') },
    { name: 'charts', path: `/hives/${ids.hive}/stats`, ready: heading('Occupied frames') },
    { name: 'tasks', path: '/tasks', ready: text('Order frames') },
    {
      name: 'scan',
      path: '/scan',
      // No camera here: the page ends on its "no camera" message and the code field.
      ready: async (page) => {
        await heading('Scan a hive label')(page);
        await expect(page.getByText('Starting camera…')).toBeHidden();
      }
    },
    { name: 'settings', path: '/settings', ready: heading('Delete account') },
    { name: 'login', path: '/login', ready: heading('Sign in'), signedOut: true }
  ];
}

async function open(page: Page, route: Route) {
  await page.goto(route.path);
  await route.ready(page);
  if (!route.signedOut) await expectSynced(page);
}

/** Violations a user would be blocked by, as "rule: element" lines. */
async function seriousAccessibilityProblems(page: Page): Promise<string[]> {
  const { violations } = await new AxeBuilder({ page }).analyze();
  return violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')}`));
}

/** Entries added to `list` since the last call. */
function fresh(list: string[]): () => string[] {
  let seen = 0;
  return () => {
    const added = list.slice(seen);
    seen = list.length;
    return added;
  };
}

for (const colorScheme of ['light', 'dark'] as const) {
  test(`${colorScheme} theme: no policy violation, console error or serious accessibility problem on the main routes`, async ({
    api,
    device
  }) => {
    test.slow();
    const { account, apiary, hive } = await seed(api);
    const context = { colorScheme, viewport: PHONE };
    const signedIn = await device({ session: account, context });
    const signedOut = await device({ context });

    for (const route of routes({ apiary, hive })) {
      await test.step(route.name, async () => {
        const { page, problems } = route.signedOut ? signedOut : signedIn;
        const csp = fresh(problems.csp);
        const errors = fresh(problems.errors);
        csp();
        errors();
        await open(page, route);
        await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
        expect
          .soft(await seriousAccessibilityProblems(page), `accessibility: ${route.name}`)
          .toEqual([]);
        expect.soft(csp(), `content security policy: ${route.name}`).toEqual([]);
        // Settings has a known console error; see the test below.
        if (route.name !== 'settings') {
          expect.soft(errors(), `console: ${route.name}`).toEqual([]);
        }
      });
    }
  });
}

// BUG (app, lib/components/settings/PasskeysSection.svelte): Settings asks for
// the account's passkeys (GET /auth/webauthn/credentials) also on a server
// that has passkeys switched off, where that endpoint does not exist. The
// answer is a 404, which the browser logs as a console error on every visit.
test.fail('settings: no console error', async ({ api, device }) => {
  const { account, apiary, hive } = await seed(api);
  const { page, problems } = await device({ session: account });
  const settings = routes({ apiary, hive }).find((r) => r.name === 'settings');
  if (!settings) throw new Error('no settings route');
  await open(page, settings);
  await page.waitForLoadState('networkidle');
  expect(problems.errors).toEqual([]);
});

// BUG (app, routes/(app)/hives/[id]/stockkarte): the record card's table sits
// in a container that scrolls sideways but cannot be focused, so it cannot
// be scrolled with the keyboard (axe rule scrollable-region-focusable, serious).
test.fail('hive record card: no serious accessibility problem', async ({ api, device }) => {
  const { account, hive } = await seed(api);
  const { page } = await device({ session: account, context: { viewport: PHONE } });
  await page.goto(`/hives/${hive}/stockkarte`);
  await expect(page.getByRole('heading', { name: 'Hive record card' })).toBeVisible();
  await expectSynced(page);
  expect(await seriousAccessibilityProblems(page)).toEqual([]);
});

/**
 * Controls that are too small for a thumb at the hive: buttons under 44 px
 * tall and text fields under 16 px (below that, iOS zooms the page on focus).
 * The sync status chip is a status line that can also be tapped, not an
 * action of the page, and is left out.
 */
function smallControls(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const found: string[] = [];
    const label = (el: Element) =>
      (el.textContent || el.getAttribute('aria-label') || el.getAttribute('name') || '')
        .trim()
        .replace(/\s+/g, ' ')
        .slice(0, 40);
    for (const el of document.querySelectorAll('button, input, select, textarea')) {
      const box = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      if (box.width === 0 || box.height === 0 || style.visibility === 'hidden') continue;
      if (el instanceof HTMLButtonElement) {
        if (el.title.includes('Sync now')) continue;
        if (box.height < 44) found.push(`button "${label(el)}": ${box.height} px tall`);
        continue;
      }
      const type = el.getAttribute('type') ?? '';
      if (['checkbox', 'radio', 'file', 'hidden'].includes(type)) continue;
      // Kept in the page for screen readers only.
      if (box.width <= 4 || box.height <= 4) continue;
      const size = parseFloat(style.fontSize);
      if (size < 16) found.push(`${el.tagName.toLowerCase()} "${label(el)}": ${size} px font`);
    }
    return found;
  });
}

test('at 360 px width buttons are at least 44 px tall and fields use at least 16 px text', async ({
  api,
  device
}) => {
  test.slow();
  const { account, apiary, hive } = await seed(api);
  const context = { viewport: PHONE };
  const signedIn = await device({ session: account, context });
  const signedOut = await device({ context });

  for (const route of routes({ apiary, hive })) {
    await test.step(route.name, async () => {
      const { page } = route.signedOut ? signedOut : signedIn;
      await open(page, route);
      expect.soft(await smallControls(page), route.name).toEqual([]);
      // No sideways scrolling of the page itself.
      expect
        .soft(await page.evaluate(() => document.documentElement.scrollWidth), route.name)
        .toBeLessThanOrEqual(PHONE.width);

      // The forms used most, opened.
      if (route.name === 'hive') {
        await page.getByRole('button', { name: 'Record visit' }).first().click();
        await expect(page.getByRole('heading', { name: 'Record visit' })).toBeVisible();
        await page.getByText('Climate', { exact: true }).click();
        await page.getByText('Activities on this visit', { exact: true }).click();
        await page.getByRole('button', { name: 'Record harvest' }).click();
        await page.getByRole('button', { name: 'Record treatment' }).click();
        await expect(page.getByLabel('Product')).toBeVisible();
        expect.soft(await smallControls(page), 'hive, forms open').toEqual([]);
      }
      if (route.name === 'apiaries') {
        await page.getByRole('button', { name: '+ New' }).click();
        await expect(page.getByLabel('Name')).toBeVisible();
        expect.soft(await smallControls(page), 'apiaries, form open').toEqual([]);
      }
    });
  }
});
