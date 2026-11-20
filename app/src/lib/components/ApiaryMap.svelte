<script lang="ts">
  import 'leaflet/dist/leaflet.css';
  import '#lib/map.css';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { goto } from '$app/navigation';
  import type * as Leaflet from 'leaflet';
  import { getMapConfig, type MapConfig } from '#lib/instance.js';
  import { createMap, pinIcon, popupHtml, type LeafletLib } from '#lib/map.js';
  import { online } from '#lib/online.js';

  type ApiaryPin = { id: string; name: string; lat: number; lng: number };

  let { apiaries = [] }: { apiaries: ApiaryPin[] } = $props();
  // Unknown until the server's map settings (or the remembered ones) are in.
  let config = $state<MapConfig | null>(null);
  let map: Leaflet.Map | null = null;
  let L: LeafletLib | null = null;
  let layer: Leaflet.LayerGroup | null = null;

  const located = $derived(apiaries.filter((a) => a.lat || a.lng));

  // Asked again when the connection returns, in case the first try failed.
  $effect(() => {
    $online;
    void getMapConfig().then((c) => (config = c));
  });

  function render() {
    if (!map || !L) return;
    if (layer) layer.remove();
    layer = L.layerGroup().addTo(map);
    const pts: Leaflet.LatLngTuple[] = [];
    for (const a of located) {
      const m = L.marker([a.lat, a.lng], { icon: pinIcon(L), title: a.name }).addTo(layer);
      m.bindPopup(popupHtml(a.name));
      m.on('click', () => goto(`/apiaries/${a.id}`));
      pts.push([a.lat, a.lng]);
    }
    const [first] = pts;
    if (pts.length === 1 && first) map.setView(first, 13);
    else if (pts.length > 1) map.fitBounds(pts, { padding: [40, 40], maxZoom: 14 });
  }

  // Builds the map when its container is shown and removes it when the
  // container goes away (offline), so Leaflet and the tiles load only then.
  function mount(el: HTMLDivElement) {
    let gone = false;
    const t = get(_);
    if (config) {
      void createMap(el, config, {
        center: [50.5, 9.5],
        zoom: 5,
        zoomIn: t('geo.zoom_in'),
        zoomOut: t('geo.zoom_out')
      }).then((created) => {
        if (gone) return created.map.remove();
        ({ L, map } = created);
        render();
      });
    }
    return {
      destroy() {
        gone = true;
        map?.remove();
        map = null;
        layer = null;
      }
    };
  }

  // Re-render markers when the apiary set changes.
  $effect(() => {
    located;
    render();
  });
</script>

<!-- Maps switched off by the operator: nothing is shown and nothing is requested. -->
{#if config?.enabled}
  {#if located.length === 0}
    <p class="hint">{$_('geo.no_coordinates')}</p>
  {:else if !$online}
    <p class="placeholder">{$_('geo.map_offline')}</p>
  {:else}
    <div class="map" use:mount></div>
  {/if}
{/if}

<style>
  .map {
    height: 320px;
    width: 100%;
    border-radius: 16px;
    overflow: hidden;
    border: 1px solid var(--border);
    z-index: 0;
  }
  .hint,
  .placeholder {
    color: var(--text-soft);
    font-size: 0.85rem;
    text-align: center;
  }
  .placeholder {
    border: 1px dashed var(--border-strong);
    border-radius: 16px;
    padding: 28px 16px;
  }
</style>
