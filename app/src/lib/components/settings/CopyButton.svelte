<script lang="ts">
  // Copies a text to the clipboard and says so for two seconds.
  import { _ } from 'svelte-i18n';
  import './settings.css';

  let { text, label = '' }: { text: string; label?: string } = $props();
  let copied = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      // Clipboard unavailable (http, permissions): the text stays selectable.
    }
  }
</script>

<button type="button" class="st-btn" onclick={copy}>
  {copied ? $_('settings.copied') : label || $_('settings.copy')}
</button>
