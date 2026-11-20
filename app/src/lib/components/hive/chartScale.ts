// Horizontal placement of chart points, shared by the line and level charts.

/**
 * x positions for points across `width` (with `pad` on both sides). With
 * timestamps the spacing follows the time between readings, so a week of
 * hourly sensor values does not stretch as wide as a season of visits;
 * without them (or when all share one instant) points are spread evenly.
 */
export function xPositions(count: number, width: number, pad: number, times: number[] = []) {
  if (count === 1) return [width / 2];
  const inner = width - 2 * pad;
  const t0 = times[0];
  const t1 = times[count - 1];
  const timed = times.length === count && t0 !== undefined && t1 !== undefined && t1 > t0;
  return Array.from({ length: count }, (_v, i) =>
    timed ? pad + (((times[i] ?? t0) - t0) / (t1 - t0)) * inner : pad + (i / (count - 1)) * inner
  );
}

/** Milliseconds of a stored date, for use as chart timestamps. */
export const timeOf = (iso: string) => new Date(iso).getTime();
