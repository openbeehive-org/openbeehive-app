<script lang="ts">
  // A compact line chart of one metric over time. Values may be negative
  // (frost); a dashed line then marks zero.
  import { xPositions } from './hive/chartScale.js';

  let {
    values,
    times = [],
    titles = [],
    label = '',
    height = 64
  }: {
    values: number[];
    /** Timestamps (ms) of the values; spaces the points by time. */
    times?: number[];
    /** Hover text per point, e.g. "12 May 2026: 34.5 kg". */
    titles?: string[];
    /** Text alternative for the whole chart. */
    label?: string;
    height?: number;
  } = $props();

  const W = 300;
  const PAD = 8;

  const chart = $derived.by(() => {
    const n = values.length;
    if (n === 0) return null;
    const min = Math.min(...values);
    const max = Math.max(...values);
    const span = max - min || 1;
    const H = height;
    const y = (v: number) => H - PAD - ((v - min) / span) * (H - 2 * PAD);
    const xs = xPositions(n, W, PAD, times);
    const pts = values.map((v, i) => ({ x: xs[i] ?? 0, y: y(v) }));
    const first = pts[0]!; // n >= 1 here
    const last = pts[n - 1]!;
    const p = (pt: { x: number; y: number }) => `${pt.x.toFixed(1)} ${pt.y.toFixed(1)}`;
    const line = pts.map((pt, i) => (i ? 'L' : 'M') + p(pt)).join(' ');
    // The fill hangs from the line down to zero when the range crosses it,
    // otherwise to the bottom edge.
    const crossesZero = min < 0 && max > 0;
    const base = crossesZero ? y(0) : H;
    const area =
      `M${first.x.toFixed(1)} ${base.toFixed(1)} ` +
      pts.map((pt) => `L${p(pt)}`).join(' ') +
      ` L${last.x.toFixed(1)} ${base.toFixed(1)} Z`;
    return { line, area, pts, zero: crossesZero ? y(0) : null };
  });
</script>

<svg
  class="spark"
  viewBox={`0 0 ${W} ${height}`}
  preserveAspectRatio="none"
  style:height={`${height}px`}
  role="img"
  aria-label={label}
>
  {#if chart}
    {#if chart.zero !== null}
      <path
        class="zero"
        d={`M0 ${chart.zero.toFixed(1)} H${W}`}
        vector-effect="non-scaling-stroke"
      />
    {/if}
    <path class="area" d={chart.area} />
    <path class="line" d={chart.line} vector-effect="non-scaling-stroke" />
    <!-- Points are zero-length round strokes: they stay circular although
         the chart is stretched to the card's width. -->
    {#each chart.pts as pt, i}
      <path
        class="dot"
        class:last={i === chart.pts.length - 1}
        d={`M${pt.x.toFixed(1)} ${pt.y.toFixed(1)} h0`}
        vector-effect="non-scaling-stroke"
      >
        {#if titles[i]}<title>{titles[i]}</title>{/if}
      </path>
    {/each}
  {/if}
</svg>

<style>
  .spark {
    width: 100%;
    display: block;
    overflow: visible;
  }
  path {
    fill: none;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .area {
    fill: var(--accent);
    opacity: 0.12;
  }
  .line {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .zero {
    stroke: var(--border-strong);
    stroke-width: 1;
    stroke-dasharray: 4 4;
  }
  .dot {
    stroke: var(--accent);
    stroke-width: 6;
  }
  .dot.last {
    stroke-width: 9;
  }
</style>
