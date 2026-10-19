// QR code helpers. A hive QR encodes a deep link `<base>/h/<hiveId>`.
// Scanning it with any phone camera opens the app at that hive.
// The hive id is a stable offline-first UUID, so the printed code never
// changes; access is still gated by sync/sharing (an id alone grants nothing).

import QRCode from 'qrcode';

// Base URL the QR points to. Falls back to the current origin so a
// self-hosted instance prints codes that point back to itself.
export function publicBase(): string {
  const env = (import.meta as any).env?.BEEHIVE_PUBLIC_URL;
  if (env) return String(env).replace(/\/$/, '');
  if (typeof location !== 'undefined') return location.origin;
  return '';
}

export function hiveUrl(hiveId: string): string {
  return `${publicBase()}/h/${hiveId}`;
}

// Short human-readable code for the printed label (display only, not routing).
export function shortCode(hiveId: string): string {
  return hiveId.replace(/-/g, '').slice(0, 6).toUpperCase();
}

// The Openbeehive bee/honeycomb mark (the app favicon), as inline SVG content
// in a 512×512 box. Ids are suffixed per call so multiple QR codes can share a
// page without gradient/clip-path id collisions.
let _markSeq = 0;
function beeMark(): string {
  const u = 'm' + (_markSeq++).toString(36);
  return `<defs>
    <linearGradient id="bg${u}" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#eeb455"/><stop offset="0.55" stop-color="#c77f22"/><stop offset="1" stop-color="#9c5d18"/>
    </linearGradient>
    <clipPath id="bd${u}"><ellipse cx="256" cy="258" rx="46" ry="60"/></clipPath>
  </defs>
  <rect width="512" height="512" rx="116" fill="url(#bg${u})"/>
  <polygon points="92,252 175,108 339,108 422,252 339,396 175,396" fill="none" stroke="#fffdf7" stroke-width="19" stroke-linejoin="round" opacity="0.9"/>
  <g fill="#fffdf7" opacity="0.95">
    <ellipse cx="212" cy="210" rx="31" ry="48" transform="rotate(-24 212 210)"/>
    <ellipse cx="300" cy="210" rx="31" ry="48" transform="rotate(24 300 210)"/>
  </g>
  <ellipse cx="256" cy="258" rx="46" ry="60" fill="#f7b733"/>
  <g clip-path="url(#bd${u})" fill="#2c2316">
    <rect x="204" y="220" width="104" height="20"/><rect x="204" y="258" width="104" height="20"/><rect x="204" y="296" width="104" height="20"/>
  </g>
  <circle cx="256" cy="198" r="23" fill="#2c2316"/>
  <g stroke="#2c2316" stroke-width="7" fill="none" stroke-linecap="round">
    <path d="M247 181 q-12 -20 -26 -25"/><path d="M265 181 q12 -20 26 -25"/>
  </g>`;
}

// Render a QR as an SVG string (offline, no network/API call), with the
// Openbeehive bee mark centred and the brand name below. Uses error-correction
// level H so the centre logo (it covers ~7% of the code) never breaks scanning.
export async function qrSvg(text: string, size = 256): Promise<string> {
  const qr = QRCode.create(text, { errorCorrectionLevel: 'H' });
  const n: number = qr.modules.size;
  const data = qr.modules.data;
  const margin = 2;
  const cell = size / (n + margin * 2);

  let d = '';
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) {
      if (data[r * n + c]) {
        const x = +((c + margin) * cell).toFixed(2);
        const y = +((r + margin) * cell).toFixed(2);
        const s = +cell.toFixed(2);
        d += `M${x} ${y}h${s}v${s}h${-s}z`;
      }
    }
  }

  const logo = size * 0.26; // centre mark size
  const lo = (size - logo) / 2;
  const ring = logo * 1.14; // white knockout behind the mark
  const ro = (size - ring) / 2;
  const textH = Math.round(size * 0.16);
  const W = size;
  const H = size + textH;

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img">
  <rect width="${W}" height="${H}" fill="#ffffff"/>
  <path d="${d}" fill="#2c2316"/>
  <rect x="${ro.toFixed(1)}" y="${ro.toFixed(1)}" width="${ring.toFixed(1)}" height="${ring.toFixed(1)}" rx="${(ring * 0.22).toFixed(1)}" fill="#ffffff"/>
  <g transform="translate(${lo.toFixed(1)} ${lo.toFixed(1)}) scale(${(logo / 512).toFixed(4)})">${beeMark()}</g>
  <text x="${(size / 2).toFixed(1)}" y="${(size + textH * 0.72).toFixed(1)}" text-anchor="middle" font-family="'Fraunces', Georgia, serif" font-weight="700" font-size="${(textH * 0.6).toFixed(1)}" fill="#2c2316">Openbeehive</text>
</svg>`;
}

// Extract a hive id from a scanned payload: full URL, custom scheme, or raw id.
export function parseHiveId(payload: string): string | null {
  const url = payload.match(/\/h\/([A-Za-z0-9-]+)/);
  if (url) return url[1] ?? null;
  const scheme = payload.match(/^openbeehive:\/\/hive\/([A-Za-z0-9-]+)/i);
  if (scheme) return scheme[1] ?? null;
  if (/^[0-9a-f]{8}-[0-9a-f-]{20,}$/i.test(payload)) return payload; // bare UUID
  return null;
}
