<script lang="ts">
  // A rating over time, drawn as named levels (best at the top) instead of
  // the stored codes, so "up" always reads as "better".
  import { xPositions } from './chartScale.js';

  let {
    levels,
    points,
    times = [],
    label = ''
  }: {
    /** The scale from best to worst. */
    levels: { code: number; label: string }[];
    /** Recorded codes, oldest to newest, with their hover text. */
    points: { value: number; title: string }[];
    /** Timestamps (ms) of the points; spaces them by time. */
    times?: number[];
    /** Text alternative for the whole chart. */
    label?: string;
  } = $props();

  const W = 300;
  const ROW = 28;
  const PAD = 8;

  const height = $derived(levels.length * ROW);
  const rowY = (i: number) => i * ROW + ROW / 2;
  const marks = $derived.by(() => {
    const xs = xPositions(points.length, W, PAD, times);
    const out: { x: number; y: number; title: string }[] = [];
    points.forEach((p, i) => {
      const row = levels.findIndex((l) => l.code === p.value);
      if (row >= 0) out.push({ x: xs[i] ?? 0, y: rowY(row), title: p.title });
    });
    return out;
  });
  const line = $derived(
    marks.map((m, i) => `${i ? 'L' : 'M'}${m.x.toFixed(1)} ${m.y.toFixed(1)}`).join(' ')
  );
</script>

<div class="levels" role="img" aria-label={label}>
  <div class="names" aria-hidden="true">
    {#each levels as l}<span style:height={`${ROW}px`}>{l.label}</span>{/each}
  </div>
  <svg viewBox={`0 0 ${W} ${height}`} preserveAspectRatio="none" style:height={`${height}px`}>
    {#each levels as _l, i}
      <path class="grid" d={`M0 ${rowY(i)} H${W}`} vector-effect="non-scaling-stroke" />
    {/each}
    <path class="line" d={line} vector-effect="non-scaling-stroke" />
    {#each marks as m, i}
      <path
        class="dot"
        class:last={i === marks.length - 1}
        d={`M${m.x.toFixed(1)} ${m.y.toFixed(1)} h0`}
        vector-effect="non-scaling-stroke"
      >
        <title>{m.title}</title>
      </path>
    {/each}
  </svg>
</div>

<style>
  .levels {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 10px;
    align-items: start;
  }
  .names {
    display: grid;
  }
  .names span {
    display: flex;
    align-items: center;
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  svg {
    width: 100%;
    display: block;
    overflow: visible;
  }
  path {
    fill: none;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .grid {
    stroke: var(--border);
    stroke-width: 1;
  }
  .line {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .dot {
    stroke: var(--accent);
    stroke-width: 8;
  }
  .dot.last {
    stroke-width: 11;
  }
</style>
