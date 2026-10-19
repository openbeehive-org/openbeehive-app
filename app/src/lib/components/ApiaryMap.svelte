<script lang="ts">
  import 'leaflet/dist/leaflet.css';
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import type * as Leaflet from 'leaflet';

  type LeafletLib = typeof import('leaflet');
  type ApiaryPin = { id: string; name: string; lat: number; lng: number };

  let { apiaries = [] }: { apiaries: ApiaryPin[] } = $props();
  let el: HTMLDivElement;
  let map: Leaflet.Map | null = null;
  let L: LeafletLib | null = null;
  let layer: Leaflet.LayerGroup | null = null;

  const located = $derived(apiaries.filter((a) => a.lat || a.lng));

  // Honey-coloured teardrop pin as an HTML marker (avoids bundler image issues).
  function pin(Lib: LeafletLib) {
    return Lib.divIcon({
      className: 'obh-pin',
      html: '<span></span>',
      iconSize: [26, 26],
      iconAnchor: [13, 26]
    });
  }

  function render() {
    if (!map || !L) return;
    if (layer) layer.remove();
    layer = L.layerGroup().addTo(map);
    const pts: Leaflet.LatLngTuple[] = [];
    for (const a of located) {
      const m = L.marker([a.lat, a.lng], { icon: pin(L) }).addTo(layer);
      m.bindPopup(`<strong>${a.name}</strong>`);
      m.on('click', () => goto(`/apiaries/${a.id}`));
      pts.push([a.lat, a.lng]);
    }
    const [first] = pts;
    if (pts.length === 1 && first) map.setView(first, 13);
    else if (pts.length > 1) map.fitBounds(pts, { padding: [40, 40], maxZoom: 14 });
  }

  onMount(async () => {
    L = (await import('leaflet')).default;
    map = L.map(el, { zoomControl: true, attributionControl: true }).setView([50.5, 9.5], 5);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution: '© OpenStreetMap'
    }).addTo(map);
    render();
    setTimeout(() => map?.invalidateSize(), 60);
  });

  // Re-render markers when the apiary set changes.
  $effect(() => {
    located;
    render();
  });

  onDestroy(() => {
    map?.remove();
    map = null;
  });
</script>

<div class="map" bind:this={el}></div>
{#if located.length === 0}
  <p class="hint">
    No apiary has coordinates yet: set a location in an apiary's settings to place it on the map.
  </p>
{/if}

<style>
  .map {
    height: 320px;
    width: 100%;
    border-radius: 16px;
    overflow: hidden;
    border: 1px solid var(--line);
    z-index: 0;
  }
  .hint {
    color: var(--ink-soft);
    font-size: 0.85rem;
    margin-top: 10px;
    text-align: center;
  }
  :global(.obh-pin span) {
    display: block;
    width: 20px;
    height: 20px;
    border-radius: 50% 50% 50% 0;
    background: var(--honey, #c77f22);
    border: 2px solid #fffdf7;
    transform: rotate(-45deg);
    box-shadow: 0 2px 5px rgba(0, 0, 0, 0.3);
  }
</style>
