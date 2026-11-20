<script lang="ts">
  // Units for weights and temperatures. The choice is an account setting: it
  // follows the user to every device. Records themselves stay metric.
  import { _ } from 'svelte-i18n';
  import { units } from '#lib/prefs.js';
  import { choose } from '#lib/local/userSettings.js';
  import { TEMP_UNITS, WEIGHT_UNITS, unitSymbol, METRIC } from '#lib/units.js';
  import './settings.css';

  const groups = [
    {
      field: 'weight_unit' as const,
      label: 'units.weight',
      options: WEIGHT_UNITS.map((v) => ({
        value: v,
        symbol: unitSymbol('weight', { ...METRIC, weight: v }),
        name: `units.${v}`
      }))
    },
    {
      field: 'temp_unit' as const,
      label: 'units.temperature',
      options: TEMP_UNITS.map((v) => ({
        value: v,
        symbol: unitSymbol('temp', { ...METRIC, temp: v }),
        name: `units.${v === 'c' ? 'celsius' : 'fahrenheit'}`
      }))
    }
  ];
</script>

<section class="st-card">
  <h2>{$_('units.title')}</h2>
  <p class="st-help">{$_('units.help')}</p>
  {#each groups as g (g.field)}
    <fieldset class="unit-group">
      <legend>{$_(g.label)}</legend>
      <div class="choices">
        {#each g.options as o (o.value)}
          {@const on = (g.field === 'weight_unit' ? $units.weight : $units.temp) === o.value}
          <label class:on>
            <input
              type="radio"
              name={g.field}
              value={o.value}
              checked={on}
              onchange={() => void choose(g.field, o.value)}
            />
            <strong>{o.symbol}</strong>
            <span>{$_(o.name)}</span>
          </label>
        {/each}
      </div>
    </fieldset>
  {/each}
  <p class="st-help">{$_('units.metric_note')}</p>
</section>

<style>
  .unit-group {
    border: 0;
    margin: 0 0 14px;
    padding: 0;
    min-width: 0;
  }
  legend {
    font-weight: 700;
    font-size: 0.92rem;
    margin-bottom: 8px;
    padding: 0;
  }
  .choices {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .choices label {
    position: relative;
    display: flex;
    align-items: baseline;
    justify-content: center;
    gap: 7px;
    min-height: 44px;
    padding: 11px 10px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }
  .choices span {
    font-size: 0.88rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* The chosen unit is filled, so it is not told by hue alone. */
  .choices label.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
  }
  .choices label:has(input:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  /* The radio stays focusable and announced; the label is what is drawn. */
  .choices input {
    position: absolute;
    inset: 0;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }
</style>
