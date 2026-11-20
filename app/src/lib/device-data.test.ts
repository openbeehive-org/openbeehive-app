import { beforeEach, describe, expect, it, vi } from 'vitest';

const wipeLocalDB = vi.fn(async () => undefined);
vi.mock('./local/db.js', () => ({ wipeLocalDB: () => wipeLocalDB() }));

const { onDeviceDataRemoval, removeDeviceData, storageId, workspaceCaches } =
  await import('./device-data');

const ORG = '4f0c2b9e-1a2b-4c3d-8e9f-001122334455';

function stubCaches(names: string[]) {
  const deleted: string[] = [];
  vi.stubGlobal('caches', {
    keys: async () => names,
    delete: async (name: string) => {
      deleted.push(name);
      return true;
    }
  });
  return deleted;
}

beforeEach(() => {
  wipeLocalDB.mockReset();
  wipeLocalDB.mockResolvedValue(undefined);
  vi.unstubAllGlobals();
});

describe('workspaceCaches', () => {
  it('picks the caches named after the workspace', () => {
    const names = ['obh-app-1.2.3', `obh-photos-${ORG}`, 'obh-photos-other-workspace'];
    expect(workspaceCaches(names, ORG)).toEqual([`obh-photos-${ORG}`]);
  });

  it('matches the file-safe form of the id', () => {
    expect(storageId('a/b c')).toBe('a_b_c');
    expect(workspaceCaches(['photos-a_b_c', 'photos-a'], 'a/b c')).toEqual(['photos-a_b_c']);
  });

  it('touches nothing without a workspace', () => {
    expect(workspaceCaches(['obh-app-1.2.3'], '')).toEqual([]);
  });
});

describe('removeDeviceData', () => {
  it('wipes the local database, the workspace caches and what modules registered', async () => {
    const deleted = stubCaches(['obh-app-1.2.3', `obh-photos-${ORG}`]);
    const cleaned: string[] = [];
    onDeviceDataRemoval(async (orgId) => void cleaned.push(orgId));

    await removeDeviceData(ORG);

    expect(wipeLocalDB).toHaveBeenCalledTimes(1);
    expect(deleted).toEqual([`obh-photos-${ORG}`]);
    expect(cleaned).toEqual([ORG]);
  });

  it('works where the browser has no cache storage', async () => {
    await removeDeviceData(ORG);
    expect(wipeLocalDB).toHaveBeenCalledTimes(1);
  });

  it('reports a failed wipe, after still clearing the rest', async () => {
    const deleted = stubCaches([`obh-photos-${ORG}`]);
    wipeLocalDB.mockRejectedValue(new Error('database is locked'));
    await expect(removeDeviceData(ORG)).rejects.toThrow('database is locked');
    expect(deleted).toEqual([`obh-photos-${ORG}`]);
  });
});
