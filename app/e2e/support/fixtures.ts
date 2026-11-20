// Test fixtures: one server per worker, an API client and browser "devices"
// (a context with its own storage, like one phone) per test.
import path from 'node:path';
import {
  test as base,
  expect,
  type BrowserContext,
  type BrowserContextOptions,
  type Page
} from '@playwright/test';
import { Api, type Session } from './api';
import { portFor, Server, urlFor, type ServerEnv } from './server';

/** What went wrong in a page without failing an action: collected for assertions. */
export type Problems = {
  /** Console messages of type "error" and uncaught exceptions. */
  errors: string[];
  /** Content-Security-Policy violations: "<directive> <blocked URI>" or the console message. */
  csp: string[];
};

export type Device = {
  context: BrowserContext;
  page: Page;
  problems: Problems;
};

export type DeviceOptions = {
  /** Signed in as this account from the first page load; omitted: signed out. */
  session?: Session & { email?: string };
  /** The server this device talks to; default: the worker's shared server. */
  server?: Server;
  /** Further context options, e.g. { viewport, colorScheme }. */
  context?: BrowserContextOptions;
};

type WorkerFixtures = {
  /** The shared server of this worker, with the default settings. */
  server: Server;
};

type TestFixtures = {
  /** A client address no other test uses (see Server: TRUST_PROXY_HEADERS). */
  clientIp: string;
  /** API client for the shared server, not signed in. */
  api: Api;
  /** The same for a server started by the test. */
  apiFor: (server: Server) => Api;
  /**
   * Starts another server with its own settings (BEEHIVE_* without the
   * prefix; a function receives the server's URL). Stopped after the test.
   */
  startServer: (env?: ServerEnv | ((url: string) => ServerEnv)) => Promise<Server>;
  /** Opens a browser context of its own: one device. Closed after the test. */
  device: (options?: DeviceOptions) => Promise<Device>;
};

let addresses = 0;
let extraServers = 0;

async function watch(context: BrowserContext, problems: Problems) {
  // Reported from inside the page. The listener sits on the window: a window
  // opened by the app keeps it when its first, empty document is replaced.
  await context.exposeBinding('__e2eCspViolation', (_source, violation: string) => {
    problems.csp.push(violation);
  });
  await context.addInitScript(() => {
    window.addEventListener('securitypolicyviolation', (e) => {
      const report = (window as unknown as Record<string, (v: string) => void>).__e2eCspViolation;
      report?.(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  context.on('console', (message) => {
    if (message.type() !== 'error') return;
    problems.errors.push(message.text());
    // A second net for violations in a document the listener missed.
    if (message.text().includes('Content Security Policy')) problems.csp.push(message.text());
  });
  context.on('weberror', (error) => problems.errors.push(String(error.error())));
}

export const test = base.extend<TestFixtures, WorkerFixtures>({
  server: [
    // eslint-disable-next-line no-empty-pattern -- Playwright requires the destructuring pattern
    async ({}, use, workerInfo) => {
      const dir = path.join(
        workerInfo.project.outputDir,
        '.servers',
        `worker-${workerInfo.workerIndex}`
      );
      const server = new Server({ port: portFor(workerInfo.parallelIndex, 0), dir });
      await server.start();
      await use(server);
      await server.stop();
    },
    { scope: 'worker' }
  ],

  // eslint-disable-next-line no-empty-pattern -- Playwright requires the destructuring pattern
  clientIp: async ({}, use, testInfo) => {
    addresses += 1;
    await use(
      `10.${testInfo.parallelIndex}.${Math.floor(addresses / 250)}.${(addresses % 250) + 1}`
    );
  },

  api: async ({ server, clientIp }, use) => {
    await use(new Api(server.url, clientIp));
  },

  apiFor: async ({ clientIp }, use) => {
    await use((server) => new Api(server.url, clientIp));
  },

  // eslint-disable-next-line no-empty-pattern -- Playwright requires the destructuring pattern
  startServer: async ({}, use, testInfo) => {
    const started: Server[] = [];
    await use(async (env = {}) => {
      // Slot 0 belongs to the shared server; the others rotate.
      const slot = 1 + (extraServers++ % 9);
      const dir = path.join(testInfo.project.outputDir, '.servers', `extra-${testInfo.testId}`);
      const port = portFor(testInfo.parallelIndex, slot);
      const settings = typeof env === 'function' ? env(urlFor(port)) : env;
      const server = new Server({ port, dir, env: settings });
      started.push(server);
      await server.start();
      return server;
    });
    for (const server of started) {
      if (testInfo.status !== testInfo.expectedStatus) {
        await testInfo.attach('extra-server.log', {
          body: server.log(),
          contentType: 'text/plain'
        });
      }
      await server.stop();
    }
  },

  device: async ({ browser, server, clientIp }, use, testInfo) => {
    const opened: BrowserContext[] = [];
    await use(async (options = {}) => {
      const origin = (options.server ?? server).url;
      const s = options.session;
      const context = await browser.newContext({
        baseURL: origin,
        extraHTTPHeaders: { 'X-Forwarded-For': clientIp },
        // The session as the sign-in page leaves it on a device.
        ...(s
          ? {
              storageState: {
                cookies: [],
                origins: [
                  {
                    origin,
                    localStorage: [
                      { name: 'session', value: s.token },
                      { name: 'obh.userId', value: s.userId },
                      { name: 'obh.orgId', value: s.orgId },
                      { name: 'obh.email', value: s.email ?? '' }
                    ]
                  }
                ]
              }
            }
          : {}),
        ...options.context
      });
      opened.push(context);
      const problems: Problems = { errors: [], csp: [] };
      await watch(context, problems);
      const page = await context.newPage();
      return { context, page, problems };
    });
    if (testInfo.status !== testInfo.expectedStatus) {
      await testInfo.attach('server.log', { body: server.log(), contentType: 'text/plain' });
    }
    for (const context of opened) await context.close();
  }
});

export { expect };
