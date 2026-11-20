// The Openbeehive export: every record of the workspace as one JSON
// document, or with photos as a ZIP of that document plus the images.
//
//   openbeehive-export.json   the document (records, photo records with
//                             their thumbnails, photos that are missing)
//   photos/<photo id>.<ext>   the full-size images
//
// The server export (GET /export) writes the same format, so either can be
// imported by importer.ts.
import { gatherAll, orgId, stamp, type Dataset } from './portability';
import { buildZip, type ZipEntry } from './zip';
import { firstSyncDone } from '../local/sync';
import { getDB } from '../local/db';

export const EXPORT_DOCUMENT = 'openbeehive-export.json';
export const PHOTO_DIR = 'photos/';
/** Version of the document layout; 1 was 0.4.x (no workspace, no photos). */
export const EXPORT_FORMAT = 2;

export type ExportDocument = {
  openbeehive_export: true;
  format: number;
  schema_version: string;
  exported_at: string;
  /** The workspace the records belong to; decides how an import treats ids. */
  workspace_id: string;
  source: 'device' | 'server';
  data: Dataset;
  /** Photos whose image is not in the archive. */
  missing_photos?: string[];
};

/** Whether this device can export now, and what the user should know. */
export type Readiness = {
  /** False until the device has loaded the workspace from the server once. */
  ready: boolean;
  /** Local changes not uploaded yet: they are part of the export. */
  pending: number;
  /** Changes the server refused (Settings, sync problems). */
  failed: number;
};

export async function exportReadiness(): Promise<Readiness> {
  const db = await getDB();
  const r = await db.get<{ pending: number; failed: number }>(
    `SELECT (SELECT COUNT(*) FROM outbox) AS pending,
            (SELECT COUNT(*) FROM outbox_failed) AS failed`
  );
  return { ready: await firstSyncDone(), pending: r?.pending ?? 0, failed: r?.failed ?? 0 };
}

export class NotSyncedError extends Error {
  constructor() {
    super('the first sync of this workspace has not finished');
    this.name = 'NotSyncedError';
  }
}

/** A full-size image for the archive, or why it cannot be had. */
export type ImageReader = (id: string) => Promise<{ blob: Blob } | { unavailable: string }>;

const EXTENSIONS: Record<string, string> = {
  'image/jpeg': '.jpg',
  'image/png': '.png',
  'image/webp': '.webp'
};

/** The archive path of a photo's image. */
export const photoPath = (id: string, contentType: string) =>
  PHOTO_DIR + encodeURIComponent(id) + (EXTENSIONS[contentType] ?? '.jpg');

export type BuiltExport = { blob: Blob; filename: string; missingPhotos: string[] };

/**
 * Builds the export of this device's copy of the workspace. With `photos`
 * the result is a ZIP with the images; images that are not on the device
 * are fetched, and those that cannot be had are listed in missingPhotos.
 */
export async function buildExport(opts: {
  photos: boolean;
  readImage?: ImageReader;
}): Promise<BuiltExport> {
  if (!(await firstSyncDone())) throw new NotSyncedError();
  const data = await gatherAll({ photos: opts.photos });
  const doc: ExportDocument = {
    openbeehive_export: true,
    format: EXPORT_FORMAT,
    schema_version: '0.5.0',
    exported_at: new Date().toISOString(),
    workspace_id: orgId(),
    source: 'device',
    data
  };
  if (!opts.photos) {
    return {
      blob: new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' }),
      filename: `openbeehive-backup-${stamp()}.json`,
      missingPhotos: []
    };
  }

  const read = opts.readImage ?? (await import('../photos/index')).photoImages.read;
  const entries: ZipEntry[] = [];
  const missing: string[] = [];
  for (const p of data.photo ?? []) {
    const got = await read(p.id).catch(() => ({ unavailable: 'error' }));
    if ('blob' in got) {
      const type = got.blob.type || p.content_type || 'image/jpeg';
      entries.push({
        name: photoPath(p.id, type),
        data: new Uint8Array(await got.blob.arrayBuffer())
      });
    } else {
      missing.push(p.id);
    }
  }
  if (missing.length) doc.missing_photos = missing;
  entries.unshift({ name: EXPORT_DOCUMENT, data: JSON.stringify(doc, null, 2) });
  return {
    blob: buildZip(entries),
    filename: `openbeehive-backup-${stamp()}.zip`,
    missingPhotos: missing
  };
}

/** The server refused or failed an export; `code` is its error code. */
export class ServerExportError extends Error {
  constructor(
    readonly status: number,
    readonly code: string
  ) {
    super(`server export failed (${status}${code ? ' ' + code : ''})`);
    this.name = 'ServerExportError';
  }
}

/**
 * Downloads the server's copy of the active workspace (GET /export), with
 * the session of this device.
 */
export async function fetchServerExport(
  photos: boolean,
  send: typeof fetch = (...a) => fetch(...a)
): Promise<{ blob: Blob; filename: string }> {
  const { apiOrigin } = await import('../account');
  const token = localStorage.getItem('session');
  let res: Response;
  try {
    res = await send(`${apiOrigin()}/export${photos ? '?photos=1' : ''}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {}
    });
  } catch {
    throw new ServerExportError(0, 'network');
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { code?: unknown };
    throw new ServerExportError(res.status, typeof body.code === 'string' ? body.code : '');
  }
  const named = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1];
  return {
    blob: await res.blob(),
    filename: named ?? `openbeehive-server-${stamp()}.${photos ? 'zip' : 'json'}`
  };
}
