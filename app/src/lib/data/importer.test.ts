import { describe, it, expect, beforeEach, vi } from 'vitest';
import { deflateRawSync } from 'node:zlib';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('../local/db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
const synced = { done: true };
vi.mock('../local/sync', () => ({
  syncOnce: vi.fn(async () => {}),
  firstSyncDone: vi.fn(async () => synced.done)
}));

import { apiaries, hives, inspections, photoRecords, patch, remove } from '../local/repo';
import { buildExport, NotSyncedError, exportReadiness, EXPORT_DOCUMENT } from './backup';
import {
  applyImport,
  lastImport,
  parseExportDocument,
  planImport,
  readImportFile,
  undoLastImport,
  type ImageSink
} from './importer';
import { readZip } from './unzip';
import { buildZip } from './zip';

const JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 5]);
const THUMB = 'data:image/jpeg;base64,AAAA';

function sink() {
  const kept = new Map<string, Blob>();
  const s: ImageSink & { kept: Map<string, Blob> } = {
    kept,
    keep: async (id, b) => void kept.set(id, b),
    drop: async (id) => void kept.delete(id)
  };
  return s;
}

const db = () => getCurrentDB();
const row = (t: string, id: string) => db().get<any>(`SELECT * FROM ${t} WHERE id = ?`, [id]);
const count = async (t: string) =>
  (await db().get<{ n: number }>(`SELECT COUNT(*) AS n FROM ${t} WHERE deleted = 0`))!.n;

async function device(org = 'org1', user = 'user1') {
  setCurrentDB(await makeLocalDB());
  localStorage.setItem('obh.orgId', org);
  localStorage.setItem('obh.userId', user);
}

// A small workspace: an apiary with a hive, a visit, an event, a personal
// task and a photo of the visit.
async function seed() {
  const a = await apiaries.create({ organization_id: 'org1', name: 'Orchard' });
  await patch('hive', 'h1', a, {
    organization_id: 'org1',
    apiary_id: a,
    name: 'Hive 1',
    deleted: 0
  });
  const v = await inspections.add({ hive_id: 'h1', date: '2026-05-01', note: 'calm' }, a);
  await patch('event', 'e1', a, {
    organization_id: 'org1',
    scope_id: a,
    apiary_id: a,
    hive_id: 'h1',
    ref_entity: 'inspection',
    ref_id: v,
    type: 1,
    deleted: 0
  });
  await patch('task', 't1', 'user:user1', {
    organization_id: 'org1',
    title: 'Order jars',
    apiary_id: '',
    assigned_to: 'user1',
    deleted: 0
  });
  await photoRecords.create(
    'p1',
    { entity: 'inspection', id: v },
    { thumb: THUMB, content_type: 'image/jpeg', size_bytes: JPEG.length }
  );
  return { a, v };
}

async function exportText(): Promise<string> {
  const built = await buildExport({ photos: false });
  return built.blob.text();
}

beforeEach(async () => {
  synced.done = true;
  await device();
});

describe('export', () => {
  it('refuses while the first sync has not finished', async () => {
    synced.done = false;
    await expect(buildExport({ photos: false })).rejects.toBeInstanceOf(NotSyncedError);
    expect((await exportReadiness()).ready).toBe(false);
  });

  it('names its workspace, counts unsent changes and leaves out photos and inline columns', async () => {
    await seed();
    const ready = await exportReadiness();
    expect(ready).toMatchObject({ ready: true, failed: 0 });
    expect(ready.pending).toBeGreaterThan(0);
    const doc = JSON.parse(await exportText());
    expect(doc).toMatchObject({ openbeehive_export: true, workspace_id: 'org1', format: 2 });
    expect(doc.data.photo).toBeUndefined();
    expect(doc.data.inspection[0]).not.toHaveProperty('photo_keys');
    expect(doc.data.hive[0]).not.toHaveProperty('photo');
  });

  it('leaves out personal tasks of another workspace', async () => {
    await patch('task', 'other', 'user:user1', { organization_id: 'org2', title: 'x', deleted: 0 });
    await patch('task', 'mine', 'user:user1', { organization_id: 'org1', title: 'y', deleted: 0 });
    const doc = JSON.parse(await exportText());
    expect(doc.data.task.map((t: any) => t.id)).toEqual(['mine']);
  });
});

describe('JSON import into the same workspace', () => {
  it('creates only what is missing and never touches existing or deleted records', async () => {
    const { a } = await seed();
    const text = await exportText();
    // Later edits on the device, and a record deleted since.
    await apiaries.rename(a, 'Orchard (renamed)');
    await remove('hive', 'h1', a);
    await db().exec(`DELETE FROM task WHERE id = 't1'`);

    const plan = await planImport(parseExportDocument(text, new Map()));
    expect(plan.foreign).toBe(false);
    expect(plan.counts.apiary).toMatchObject({ create: 0, skip: 1 });
    expect(plan.counts.hive).toMatchObject({ create: 0, skip: 1 });
    expect(plan.counts.task).toMatchObject({ create: 1 });
    await applyImport(plan);

    expect((await row('apiary', a)).name).toBe('Orchard (renamed)');
    expect((await row('hive', 'h1')).deleted).toBe(1);
    expect((await row('task', 't1')).title).toBe('Order jars');
  });

  it('overwrites only differing fields, keeps deleted records deleted unless restoring', async () => {
    const { a, v } = await seed();
    const text = await exportText();
    await apiaries.update(a, { name: 'Renamed', note: 'kept' });
    await remove('hive', 'h1', a);
    // The file does not mention the note at all.
    const doc = JSON.parse(text);
    delete doc.data.apiary[0].note;

    let plan = await planImport(parseExportDocument(JSON.stringify(doc), new Map()), {
      overwrite: true
    });
    expect(plan.counts.apiary).toMatchObject({ update: 1 });
    expect(plan.counts.hive).toMatchObject({ skip: 1, restore: 0 });
    expect(plan.counts.inspection).toMatchObject({ skip: 1 }); // unchanged
    const apiaryOp = plan.ops.find((o) => o.entity === 'apiary')!;
    expect(Object.keys(apiaryOp.fields).sort()).toEqual(['name', 'updated_at']);
    await applyImport(plan);
    expect(await row('apiary', a)).toMatchObject({ name: 'Orchard', note: 'kept' });
    expect((await row('hive', 'h1')).deleted).toBe(1);

    plan = await planImport(parseExportDocument(text, new Map()), {
      overwrite: true,
      restoreDeleted: true
    });
    expect(plan.counts.hive).toMatchObject({ restore: 1 });
    await applyImport(plan);
    expect((await row('hive', 'h1')).deleted).toBe(0);
    expect((await row('inspection', v)).deleted).toBe(0);
  });
});

describe('JSON import from another workspace', () => {
  it('gives every record a new id and keeps the references between them', async () => {
    const { a, v } = await seed();
    const text = await exportText();

    await device('org2', 'user2');
    // A record with the same id exists here already: it must not be hit.
    await patch('apiary', a, a, { organization_id: 'org2', name: 'Not mine', deleted: 0 });
    const plan = await planImport(parseExportDocument(text, new Map()));
    expect(plan.foreign).toBe(true);
    expect(plan.workspace).toBe('org2');
    expect(plan.total.skip).toBe(0);
    await applyImport(plan);

    expect((await row('apiary', a)).name).toBe('Not mine');
    const ap = await db().get<any>(`SELECT * FROM apiary WHERE name = 'Orchard'`);
    expect(ap.id).not.toBe(a);
    expect(ap.organization_id).toBe('org2');
    const hive = await db().get<any>(`SELECT * FROM hive WHERE name = 'Hive 1'`);
    expect(hive.id).not.toBe('h1');
    expect(hive.apiary_id).toBe(ap.id);
    const visit = await db().get<any>(`SELECT * FROM inspection`);
    expect(visit.id).not.toBe(v);
    expect(visit.hive_id).toBe(hive.id);
    const ev = await db().get<any>(`SELECT * FROM event`);
    expect(ev).toMatchObject({
      scope_id: ap.id,
      apiary_id: ap.id,
      hive_id: hive.id,
      ref_id: visit.id
    });
    const task = await db().get<any>(`SELECT * FROM task`);
    expect(task).toMatchObject({ organization_id: 'org2', assigned_to: '' });
    // Scopes in the outbox follow the new ids.
    const scopes = await db().all<any>(
      `SELECT entity, scope_id FROM outbox WHERE entity_id IN (?, ?)`,
      [hive.id, task.id]
    );
    expect(scopes).toEqual(
      expect.arrayContaining([
        { entity: 'hive', scope_id: ap.id },
        { entity: 'task', scope_id: 'user:user2' }
      ])
    );

    // The same file again: the same new ids, so nothing new.
    const again = await planImport(parseExportDocument(text, new Map()));
    expect(again.total.create).toBe(0);
  });

  it('treats an old export without a workspace by the workspace of its rows', async () => {
    await seed();
    const doc = JSON.parse(await exportText());
    delete doc.workspace_id;
    const parsed = parseExportDocument(JSON.stringify({ data: doc.data }), new Map());
    expect(parsed.fromWorkspace).toBe('org1');
    expect((await planImport(parsed)).foreign).toBe(false);
  });

  it('rejects files that are not exports', () => {
    expect(() => parseExportDocument('{"hello": 1}', new Map())).toThrow(/not an Openbeehive/);
    expect(() => parseExportDocument('nope', new Map())).toThrow(/not valid JSON/);
  });
});

describe('export with photos', () => {
  it('round-trips records, thumbnails and images through the ZIP', async () => {
    const { v } = await seed();
    await photoRecords.create(
      'p2',
      { entity: 'hive', id: 'h1' },
      { thumb: THUMB, content_type: 'image/jpeg', size_bytes: 3 }
    );
    const built = await buildExport({
      photos: true,
      readImage: async (id) =>
        id === 'p1'
          ? { blob: new Blob([JPEG], { type: 'image/jpeg' }) }
          : { unavailable: 'offline' }
    });
    expect(built.missingPhotos).toEqual(['p2']);
    const files = await readZip(new Uint8Array(await built.blob.arrayBuffer()));
    expect([...files.keys()].sort()).toEqual([EXPORT_DOCUMENT, 'photos/p1.jpg']);

    await device('org1', 'user1');
    const parsed = await readImportFile(built.blob);
    expect(parsed.kind).toBe('zip');
    const plan = await planImport(parsed);
    expect(plan.counts.photo).toMatchObject({ create: 2 });
    expect(plan.photosWithoutImage).toBe(1);
    const images = sink();
    await applyImport(plan, images);

    expect(await photoRecords.of('inspection', v)).toMatchObject([{ id: 'p1', thumb: THUMB }]);
    expect((await photoRecords.hivePicture('h1'))?.id).toBe('p2');
    expect([...images.kept.keys()]).toEqual(['p1']);
    expect(new Uint8Array(await images.kept.get('p1')!.arrayBuffer())).toEqual(JPEG);
    // The photo syncs in the scope of the visit's apiary.
    const out = await db().get<any>(`SELECT scope_id FROM outbox WHERE entity_id = 'p1'`);
    expect(out.scope_id).toBe((await row('hive', 'h1')).apiary_id);
  });

  it('reads archives whose entries are deflated', async () => {
    await seed();
    const text = await exportText();
    const zip = deflatedZip([
      [EXPORT_DOCUMENT, new TextEncoder().encode(text)],
      ['photos/p1.jpg', JPEG]
    ]);
    const files = await readZip(zip);
    expect(new TextDecoder().decode(files.get(EXPORT_DOCUMENT))).toBe(text);
    expect(files.get('photos/p1.jpg')).toEqual(JPEG);
    // A plain store archive reads as well.
    const stored = buildZip([{ name: 'a.txt', data: 'hello' }]);
    const back = await readZip(new Uint8Array(await stored.arrayBuffer()));
    expect(new TextDecoder().decode(back.get('a.txt'))).toBe('hello');
  });
});

describe('CSV import', () => {
  const CSV = [
    'Apiary,Hive,Date,Notes,Varroa,Weight',
    ' orchard ,HIVE 1,2026-05-01,calm,3,40.5',
    'Orchard,Hive 2,2026-05-02,busy,,',
    'Orchard,Hive 2,2026-05-02,busy,,'
  ].join('\n');

  it('matches apiaries and hives by name and creates nothing on a second run', async () => {
    const a = await apiaries.create({ organization_id: 'org1', name: 'Orchard' });
    await patch('hive', 'h1', a, {
      organization_id: 'org1',
      apiary_id: a,
      name: 'Hive 1',
      deleted: 0
    });

    const first = await planImport(await readImportFile(new Blob([CSV])));
    expect(first.foreign).toBe(false);
    expect(first.counts.apiary).toMatchObject({ create: 0, skip: 1 });
    expect(first.counts.hive).toMatchObject({ create: 1, skip: 1 });
    expect(first.counts.inspection).toMatchObject({ create: 2 });
    await applyImport(first);
    expect(await count('apiary')).toBe(1);
    expect(await count('hive')).toBe(2);
    expect(await count('inspection')).toBe(2);
    const visit = await db().get<any>(`SELECT * FROM inspection WHERE hive_id = 'h1'`);
    expect(visit).toMatchObject({ note: 'calm', varroa: '3', weight_kg: 40.5 });

    const second = await planImport(await readImportFile(new Blob([CSV])), { overwrite: true });
    expect(second.total.create).toBe(0);
    expect(second.total.update).toBe(0);
    await applyImport(second);
    expect(await count('inspection')).toBe(2);
  });

  it('records a visit again when its values differ', async () => {
    await applyImport(await planImport(await readImportFile(new Blob([CSV]))));
    const changed = CSV.replace('calm,3', 'calm,4');
    const plan = await planImport(await readImportFile(new Blob([changed])));
    expect(plan.counts.inspection).toMatchObject({ create: 1 });
  });
});

describe('undo', () => {
  it('deletes exactly what the import created, keeping what got records of its own', async () => {
    const { a } = await seed();
    const text = await exportText();
    await device('org2', 'user2');
    const own = await apiaries.create({ organization_id: 'org2', name: 'Own' });
    const images = sink();
    expect(await lastImport()).toBeNull();
    const plan = await planImport(parseExportDocument(text, new Map()));
    await applyImport(plan, images);
    const created = (await lastImport())!;
    expect(created.created).toBe(plan.total.create);

    const hive = await db().get<any>(`SELECT * FROM hive WHERE name = 'Hive 1'`);
    // A visit recorded after the import keeps its hive and that hive's apiary.
    await inspections.add({ hive_id: hive.id, date: '2026-06-01' }, hive.apiary_id);

    const res = await undoLastImport(images);
    expect(res.kept).toBe(2);
    expect(res.removed).toBe(plan.total.create - 2);
    expect(await count('apiary')).toBe(2);
    expect((await hives.get(hive.id))?.name).toBe('Hive 1');
    expect(await count('task')).toBe(0);
    expect(await count('event')).toBe(0);
    expect((await apiaries.get(own))?.name).toBe('Own');
    expect(await lastImport()).toBeNull();
    expect(a).toBeTruthy();
  });
});

// A ZIP with deflated entries, as zip tools and the server write them.
function deflatedZip(files: [string, Uint8Array][]): Uint8Array {
  const enc = new TextEncoder();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;
  for (const [name, data] of files) {
    const n = enc.encode(name);
    const body = new Uint8Array(deflateRawSync(data));
    const local = new Uint8Array(30 + n.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(8, 8, true);
    lv.setUint32(18, body.length, true);
    lv.setUint32(22, data.length, true);
    lv.setUint16(26, n.length, true);
    local.set(n, 30);
    const cd = new Uint8Array(46 + n.length);
    const cv = new DataView(cd.buffer);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(10, 8, true);
    cv.setUint32(20, body.length, true);
    cv.setUint32(24, data.length, true);
    cv.setUint16(28, n.length, true);
    cv.setUint32(42, offset, true);
    cd.set(n, 46);
    parts.push(local, body);
    central.push(cd);
    offset += local.length + body.length;
  }
  const size = central.reduce((s, c) => s + c.length, 0);
  const end = new Uint8Array(22);
  const ev = new DataView(end.buffer);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, files.length, true);
  ev.setUint16(10, files.length, true);
  ev.setUint32(12, size, true);
  ev.setUint32(16, offset, true);
  const all = [...parts, ...central, end];
  const out = new Uint8Array(all.reduce((s, p) => s + p.length, 0));
  let at = 0;
  for (const p of all) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}
