<script lang="ts">
  // Edit / Delete on a record card. `name` identifies the record for
  // screen readers ("Edit: 12 kg, 3 Jul 2026").
  import { _ } from 'svelte-i18n';
  import { Pencil, Trash2 } from '@lucide/svelte';
  import { demoSession } from '#lib/demo.js';
  import './hive.css';

  let { name, onedit, ondelete }: { name: string; onedit: () => void; ondelete: () => void } =
    $props();
</script>

<div class="hv-racts">
  <button
    type="button"
    class="hv-link"
    aria-label={`${$_('hives.act_edit')}: ${name}`}
    onclick={onedit}
  >
    <Pencil size={16} aria-hidden="true" />
    {$_('hives.act_edit')}
  </button>
  <button
    type="button"
    class="hv-link danger"
    aria-label={`${$_('common.delete')}: ${name}`}
    disabled={$demoSession}
    onclick={ondelete}
  >
    <Trash2 size={16} aria-hidden="true" />
    {$_('common.delete')}
  </button>
</div>
