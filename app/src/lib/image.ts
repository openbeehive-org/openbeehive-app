// Turns a picked image file into what a photo record needs: a full-size
// image for the blob store and a small thumbnail that syncs with the record.
// Both are drawn anew on a canvas, which also drops whatever the camera
// wrote into the file besides the picture (location, device, time).

/** Longer edge of the full-size image. */
export const FULL_EDGE = 2048;
/** Longer edge of the thumbnail. */
export const THUMB_EDGE = 200;
/** What the server accepts as a full-size image. */
export const MAX_FULL_BYTES = 5 * 1024 * 1024;
/** Upper bound for the thumbnail data URL; the server refuses more than 48 KiB. */
export const MAX_THUMB_CHARS = 40 * 1024;

export const PHOTO_TYPE = 'image/jpeg';

export interface PreparedPhoto {
  /** The image to upload: JPEG, at most FULL_EDGE on its longer edge. */
  full: Blob;
  /** The thumbnail as a data URL. */
  thumb: string;
  contentType: string;
}

/** The size that fits `max` on the longer edge; never enlarges. */
export function fitWithin(
  width: number,
  height: number,
  max: number
): { width: number; height: number } {
  const scale = Math.min(1, max / Math.max(width, height, 1));
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale))
  };
}

// Qualities tried in turn until the result is small enough.
const QUALITIES = [0.85, 0.75, 0.6, 0.45];

/**
 * Encodes with decreasing quality until `fits` accepts the result, and
 * returns the last attempt if none does.
 */
export async function encodeWithin<T>(
  encode: (quality: number) => Promise<T>,
  fits: (result: T) => boolean
): Promise<T> {
  let out = await encode(QUALITIES[0]!);
  for (const q of QUALITIES.slice(1)) {
    if (fits(out)) break;
    out = await encode(q);
  }
  return out;
}

function draw(bitmap: ImageBitmap, max: number): HTMLCanvasElement {
  const { width, height } = fitWithin(bitmap.width, bitmap.height, max);
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('canvas unavailable');
  // JPEG has no transparency: put white behind transparent pictures.
  ctx.fillStyle = 'white';
  ctx.fillRect(0, 0, width, height);
  ctx.drawImage(bitmap, 0, 0, width, height);
  return canvas;
}

const toBlob = (canvas: HTMLCanvasElement, quality: number) =>
  new Promise<Blob>((resolve, reject) =>
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error('could not encode the image'))),
      PHOTO_TYPE,
      quality
    )
  );

/** Reads a picked file and produces the full-size image and its thumbnail. */
export async function preparePhoto(file: Blob): Promise<PreparedPhoto> {
  // The picture as the camera meant it: rotated by its orientation flag.
  const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' });
  try {
    const large = draw(bitmap, FULL_EDGE);
    const full = await encodeWithin(
      (q) => toBlob(large, q),
      (blob) => blob.size <= MAX_FULL_BYTES
    );
    if (full.size > MAX_FULL_BYTES) throw new Error('the image is too large');
    const small = draw(bitmap, THUMB_EDGE);
    const thumb = await encodeWithin(
      async (q) => small.toDataURL(PHOTO_TYPE, q),
      (url) => url.length <= MAX_THUMB_CHARS
    );
    return { full, thumb, contentType: PHOTO_TYPE };
  } finally {
    bitmap.close?.();
  }
}
