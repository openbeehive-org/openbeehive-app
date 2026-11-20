<script lang="ts">
  // A weight or temperature input in the units the form was opened with.
  // The text is read back with lib/units.ts: in the decimal convention of
  // the app's language, converted to metric, and unchanged as long as the
  // user does not touch it.
  import { _ } from 'svelte-i18n';
  import {
    readField,
    unitSymbol,
    type Quantity,
    type QuantityField,
    type Units
  } from '#lib/units.js';
  import './hive.css';

  let {
    label,
    quantity,
    units,
    locale,
    field = $bindable(),
    required = false
  }: {
    label: string;
    quantity: Quantity;
    units: Units;
    locale: string | null | undefined;
    field: QuantityField;
    required?: boolean;
  } = $props();

  const invalid = $derived(readField(quantity, field, units, locale) === undefined);
</script>

<label class="hv-field">
  <span>{label} ({unitSymbol(quantity, units)})</span>
  <!-- Text rather than type=number, so the language's decimal comma works.
       Temperatures get the full keyboard: the decimal keypad of iOS has no
       minus key. -->
  <input
    type="text"
    inputmode={quantity === 'weight' ? 'decimal' : 'text'}
    autocomplete="off"
    spellcheck="false"
    bind:value={field.text}
    aria-invalid={invalid}
    {required}
  />
  {#if invalid}<span class="hv-hint bad">{$_('units.invalid')}</span>{/if}
</label>

<style>
  .bad {
    color: var(--danger);
  }
</style>
