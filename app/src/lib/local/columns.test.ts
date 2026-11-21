// The app's local schema and the server's column whitelist must agree on
// what is synced. Push drops a column the server does not know without a
// word, and Pull skips one the app has no column for, so a mismatch loses
// data silently. server/internal/service/testdata/synced_columns.json is
// written by the Go test TestSyncedColumnsFile from the server's list.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { makeLocalDB } from '../../tests/localdb';
import { SYNCED_TABLES, columnsOf, setColsOf } from './tables';

const server: Record<string, { cols: string[]; set_cols: string[] }> = JSON.parse(
  readFileSync(
    new URL('../../../../server/internal/service/testdata/synced_columns.json', import.meta.url),
    'utf8'
  )
);

// Columns that live on this device only and are never sent.
const LOCAL_ONLY = new Set(['id', 'field_hlc']);

describe('synced columns', () => {
  it('the synced tables are the server entities', () => {
    expect([...SYNCED_TABLES].sort()).toEqual(Object.keys(server).sort());
  });

  for (const entity of SYNCED_TABLES) {
    it(`${entity}: the local columns are the server columns`, async () => {
      const db = await makeLocalDB();
      const local = [...(await columnsOf(db, entity))!].filter((c) => !LOCAL_ONLY.has(c)).sort();
      expect(local).toEqual(server[entity]!.cols);
      expect([...setColsOf(entity)].sort()).toEqual(server[entity]!.set_cols);
    });
  }
});
