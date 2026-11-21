// Minimal ZIP reader for the export archives: stored entries (what zip.ts
// writes) and deflated ones (what the server and most zip tools write),
// found through the central directory. No ZIP64, no encryption.

import { CodedError } from '../errors';

const dec = new TextDecoder();

const damaged = () => new CodedError('zip_damaged', 'damaged ZIP archive');

const EOCD = 0x06054b50;
const CENTRAL = 0x02014b50;
const LOCAL = 0x04034b50;

/** Whether the bytes start like a ZIP archive. */
export function looksLikeZip(bytes: Uint8Array): boolean {
  return (
    bytes.length >= 4 && bytes[0] === 0x50 && bytes[1] === 0x4b && bytes[2] === 3 && bytes[3] === 4
  );
}

async function inflate(data: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([data as BlobPart])
    .stream()
    .pipeThrough(new DecompressionStream('deflate-raw'));
  try {
    return new Uint8Array(await new Response(stream).arrayBuffer());
  } catch {
    throw damaged();
  }
}

/** The files of a ZIP archive by name. Directories are left out. */
export async function readZip(bytes: Uint8Array): Promise<Map<string, Uint8Array>> {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  // The end record sits in the last 22 bytes plus at most a 64 KiB comment.
  let end = -1;
  for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 22 - 0xffff); i--) {
    if (view.getUint32(i, true) === EOCD) {
      end = i;
      break;
    }
  }
  if (end < 0) throw damaged();
  const count = view.getUint16(end + 10, true);
  let pos = view.getUint32(end + 16, true);

  const out = new Map<string, Uint8Array>();
  for (let n = 0; n < count; n++) {
    if (pos + 46 > bytes.length || view.getUint32(pos, true) !== CENTRAL) {
      throw damaged();
    }
    const flags = view.getUint16(pos + 8, true);
    const method = view.getUint16(pos + 10, true);
    const size = view.getUint32(pos + 20, true);
    const nameLen = view.getUint16(pos + 28, true);
    const extraLen = view.getUint16(pos + 30, true);
    const commentLen = view.getUint16(pos + 32, true);
    const local = view.getUint32(pos + 42, true);
    const name = dec.decode(bytes.subarray(pos + 46, pos + 46 + nameLen));
    pos += 46 + nameLen + extraLen + commentLen;

    if (name.endsWith('/')) continue;
    if (flags & 1)
      throw new CodedError('zip_unsupported', 'encrypted ZIP entries are not supported');
    if (local + 30 > bytes.length || view.getUint32(local, true) !== LOCAL) {
      throw damaged();
    }
    const start = local + 30 + view.getUint16(local + 26, true) + view.getUint16(local + 28, true);
    const data = bytes.subarray(start, start + size);
    if (data.length !== size) throw damaged();
    let file: Uint8Array;
    if (method === 0) file = data;
    else if (method === 8) file = await inflate(data);
    else throw new CodedError('zip_unsupported', `unsupported ZIP compression method ${method}`);
    out.set(name, file);
  }
  return out;
}
