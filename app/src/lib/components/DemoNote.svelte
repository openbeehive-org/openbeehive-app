<script lang="ts">
  // In the demo: says that the form cannot be saved. Renders nothing otherwise.
  import { _ } from 'svelte-i18n';
  import { demoSession } from '#lib/demo.js';
</script>

{#if $demoSession}
  <p class="demo-note">{$_('demo.form_note')}</p>
{/if}

<style>
  .demo-note {
    margin: 0 0 12px;
    padding: 10px 12px;
    border-radius: 12px;
    background: var(--warn-bg);
    color: var(--warn-text);
    font-size: 0.9rem;
    font-weight: 600;
  }
</style>
