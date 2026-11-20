// Starts the real server binary (with the embedded app) for the browser
// tests: a fresh SQLite database and blob directory per instance, password
// login with an instance admin, no mail server, maps off.
import { spawn, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '../../..');

/** The single binary built by `make build`. */
export const BINARY =
  process.env.E2E_SERVER_BINARY ?? path.join(repoRoot, 'server/bin/openbeehive');
/** The app build embedded in the binary (`make build` copies it here). */
export const WEB_DIST = process.env.E2E_WEB_DIST ?? path.join(repoRoot, 'server/internal/web/dist');

export const ADMIN = { email: 'admin@e2e.test', password: 'e2e-admin-password' };

// Every worker owns a block of ports: the first for its shared server, the
// rest for servers a single test starts with its own settings.
const PORT_BASE = Number(process.env.E2E_PORT_BASE ?? 18400);
const PORTS_PER_WORKER = 10;

export function portFor(parallelIndex: number, slot: number): number {
  if (slot < 0 || slot >= PORTS_PER_WORKER) throw new Error(`no port slot ${slot}`);
  return PORT_BASE + parallelIndex * PORTS_PER_WORKER + slot;
}

/**
 * The address the app is opened at. "localhost", not 127.0.0.1: passkeys
 * need a host name as relying-party id, and the server derives it from here.
 */
export const urlFor = (port: number): string => `http://localhost:${port}`;

/** BEEHIVE_* settings without the prefix, e.g. { DEMO: 'true' }. */
export type ServerEnv = Record<string, string>;

export type ServerOptions = {
  port: number;
  /** Directory for the database, blobs and log; created if missing. */
  dir: string;
  /** Settings on top of the defaults in start(). */
  env?: ServerEnv;
};

export class Server {
  readonly url: string;
  private child: ChildProcess | null = null;
  private exited: Promise<void> = Promise.resolve();
  private readonly logFile: string;
  // Sessions must survive a restart of the same instance.
  private readonly sessionSecret = randomBytes(32).toString('hex');

  constructor(private readonly options: ServerOptions) {
    this.url = urlFor(options.port);
    this.logFile = path.join(options.dir, 'server.log');
  }

  /** Starts the server and waits until it answers. `env` overrides the options' settings. */
  async start(env: ServerEnv = {}): Promise<void> {
    if (this.child) throw new Error('server already running');
    const { port, dir } = this.options;
    fs.mkdirSync(dir, { recursive: true });

    const settings: ServerEnv = {
      DEPLOYMENT_PROFILE: 'selfhost',
      ADDR: `127.0.0.1:${port}`,
      PUBLIC_BASE_URL: this.url,
      DATABASE_DRIVER: 'sqlite',
      DATABASE_DSN: `file:${path.join(dir, 'e2e.db')}?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`,
      BLOB_BACKEND: 'fs',
      BLOB_DIR: path.join(dir, 'blobs'),
      PASSWORD_AUTH: 'true',
      REGISTRATION: 'true',
      SESSION_SECRET: this.sessionSecret,
      ADMIN_EMAIL: ADMIN.email,
      ADMIN_PASSWORD: ADMIN.password,
      // Nothing may leave the machine: no map tiles, no geocoder, no mail.
      MAPS: 'off',
      // Tests send their own client address (X-Forwarded-For), so the
      // sign-in throttle counts every test separately.
      TRUST_PROXY_HEADERS: 'true',
      HTTP_SHUTDOWN_TIMEOUT: '2s',
      ...this.options.env,
      ...env
    };
    // The developer's own BEEHIVE_* variables must not leak into the test server.
    const inherited = Object.entries(process.env).filter(([key]) => !key.startsWith('BEEHIVE_'));
    const log = fs.openSync(this.logFile, 'a');
    const child = spawn(BINARY, [], {
      // An empty working directory: no .env file is picked up.
      cwd: dir,
      env: {
        ...Object.fromEntries(inherited),
        ...Object.fromEntries(Object.entries(settings).map(([k, v]) => [`BEEHIVE_${k}`, v]))
      },
      stdio: ['ignore', log, log]
    });
    fs.closeSync(log);
    this.child = child;
    let exitCode: number | null | undefined;
    this.exited = new Promise((resolve) => {
      child.once('exit', (code) => {
        exitCode = code;
        resolve();
      });
      child.once('error', () => {
        exitCode = -1;
        resolve();
      });
    });

    const deadline = Date.now() + 20_000;
    for (;;) {
      if (exitCode !== undefined) {
        this.child = null;
        throw new Error(`server exited with code ${exitCode} while starting:\n${this.log()}`);
      }
      try {
        const res = await fetch(`${this.url}/healthz`, { signal: AbortSignal.timeout(1000) });
        if (res.ok) return;
      } catch {
        // not listening yet
      }
      if (Date.now() > deadline) {
        await this.stop();
        throw new Error(`server did not answer on ${this.url} within 20 s:\n${this.log()}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }

  /** Stops the server; the database stays, so start() continues with it. */
  async stop(): Promise<void> {
    const child = this.child;
    if (!child) return;
    this.child = null;
    child.kill('SIGTERM');
    const killer = setTimeout(() => child.kill('SIGKILL'), 5000);
    await this.exited;
    clearTimeout(killer);
  }

  /**
   * Changes the database directly. Only for states no endpoint can produce
   * (an account without a password exists only after a provider sign-in).
   */
  sql(statement: string, ...params: (string | number | null)[]): void {
    const db = new DatabaseSync(path.join(this.options.dir, 'e2e.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      db.prepare(statement).run(...params);
    } finally {
      db.close();
    }
  }

  log(): string {
    try {
      return fs.readFileSync(this.logFile, 'utf8');
    } catch {
      return '';
    }
  }
}

/**
 * Writes the next version of the app into `dir` and returns its version.
 * Two builds of the same sources differ only in the build version, which
 * SvelteKit puts into the app shell, the service worker and version.json;
 * this makes that second build from the embedded one without running Vite.
 */
export function writeNextAppVersion(dir: string): string {
  fs.cpSync(WEB_DIST, dir, { recursive: true });
  const versionFile = path.join(dir, '_app/version.json');
  const current = (JSON.parse(fs.readFileSync(versionFile, 'utf8')) as { version: string }).version;
  const next = String(Math.max(Date.now(), Number(current) + 1));
  for (const file of ['index.html', 'service-worker.js', '_app/version.json']) {
    const target = path.join(dir, file);
    const text = fs.readFileSync(target, 'utf8');
    if (!text.includes(current)) throw new Error(`${file} does not carry the build version`);
    fs.writeFileSync(target, text.replaceAll(current, next));
  }
  return next;
}
