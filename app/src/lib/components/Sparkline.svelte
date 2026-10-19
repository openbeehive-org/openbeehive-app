<script lang="ts">
  // A compact line+area chart of a metric's development over time.
  let {
    values,
    color = '#c77f22',
    height = 60
  }: { values: number[]; color?: string; height?: number } = $props();

  const W = 300;
  const PAD = 6;

  const chart = $derived.by(() => {
    const n = values.length;
    if (n === 0) return { line: '', area: '', last: null as null | { x: number; y: number } };
    const min = Math.min(...values);
    const max = Math.max(...values);
    const span = max - min || 1;
    const H = height;
    const pts = values.map((v, i) => {
      const x = n === 1 ? W / 2 : PAD + (i / (n - 1)) * (W - 2 * PAD);
      const y = H - PAD - ((v - min) / span) * (H - 2 * PAD);
      return { x, y };
    });
    const first = pts[0]!; // n >= 1 here
    const last = pts[n - 1]!;
    const line = pts
      .map((p, i) => (i ? 'L' : 'M') + p.x.toFixed(1) + ' ' + p.y.toFixed(1))
      .join(' ');
    const area =
      `M${first.x.toFixed(1)} ${H} ` +
      pts.map((p) => `L${p.x.toFixed(1)} ${p.y.toFixed(1)}`).join(' ') +
      ` L${last.x.toFixed(1)} ${H} Z`;
    return { line, area, last };
  });
</script>

<svg
  class="spark"
  viewBox={`0 0 ${W} ${height}`}
  preserveAspectRatio="none"
  style={`height:${height}px`}
>
  {#if chart.last}
    <path d={chart.area} fill={color} opacity="0.12" />
    <path
      d={chart.line}
      fill="none"
      stroke={color}
      stroke-width="2"
      vector-effect="non-scaling-stroke"
      stroke-linejoin="round"
      stroke-linecap="round"
    />
    <circle
      cx={chart.last.x}
      cy={chart.last.y}
      r="3.5"
      fill={color}
      vector-effect="non-scaling-stroke"
    />
  {/if}
</svg>

<style>
  .spark {
    width: 100%;
    display: block;
  }
</style>
