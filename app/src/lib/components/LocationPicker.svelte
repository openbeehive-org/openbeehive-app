<script lang="ts">
  import 'leaflet/dist/leaflet.css';
  import { onMount, onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { geocode, reverseGeocode, fmtCoords } from '#lib/geocode.js';
  import { Check } from '@lucide/svelte';
  import type * as Leaflet from 'leaflet';

  type LeafletLib = typeof import('leaflet');

  // Two-way bound: the parent's address input and the pin stay in sync.
  // Typing an address geocodes it and moves the pin; moving the pin (drag or
  // map click) reverse-geocodes and rewrites the address, or falls back to
  // plain GPS coordinates when no address is known for the spot.
  let {
    lat = $bindable(0),
    lng = $bindable(0),
    address = $bindable('')
  }: { lat?: number; lng?: number; address?: string } = $props();

  let el: HTMLDivElement;
  let map: Leaflet.Map | null = null;
  let L: LeafletLib | null = null;
  let marker: Leaflet.Marker | null = null;
  // Lookup feedback shown under the map: idle (hint) | searching | found |
  // none (service knows no match) | unreachable (offline / blocked).
  let lookup = $state<'idle' | 'searching' | 'found' | 'none' | 'unreachable'>('idle');

  // The address value we last wrote ourselves (reverse geocode). Seeing it
  // again in the effect means "not user input": do not geocode it back.
  let selfWritten = '';
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;

  function pin(Lib: LeafletLib) {
    return Lib.divIcon({
      className: 'obh-pin',
      html: '<span></span>',
      iconSize: [26, 26],
      iconAnchor: [13, 26]
    });
  }

  function placeMarker(a: number, b: number, pan = true) {
    if (!map || !L) return;
    if (!marker) {
      const m = L.marker([a, b], { icon: pin(L), draggable: true }).addTo(map);
      m.on('dragend', () => {
        const p = m.getLatLng();
        pinMoved(p.lat, p.lng);
      });
      marker = m;
    } else {
      marker.setLatLng([a, b]);
    }
    if (pan) map.setView([a, b], Math.max(map.getZoom(), 14));
  }

  async function pinMoved(a: number, b: number) {
    lat = +a.toFixed(6);
    lng = +b.toFixed(6);
    lookup = 'searching';
    const res = await reverseGeocode(lat, lng);
    // No address for the spot (or no network): fall back to raw coordinates;
    // the position itself is already set either way.
    const label = res.status === 'found' ? res.value : fmtCoords(lat, lng);
    selfWritten = label;
    address = label;
    lookup = res.status === 'unreachable' ? 'unreachable' : 'found';
  }

  // Coordinates changed from outside (numeric inputs, geolocation button) ->
  // move the pin. A no-op when the pin itself caused the change.
  $effect(() => {
    const a = lat,
      b = lng;
    if (!map || (!a && !b)) return;
    const cur = marker?.getLatLng();
    if (!cur || Math.abs(cur.lat - a) > 1e-6 || Math.abs(cur.lng - b) > 1e-6) {
      placeMarker(a, b);
    }
  });

  // Address typed by the user -> geocode (debounced) -> move the pin.
  $effect(() => {
    const a = address;
    if (!map || a === selfWritten || a.trim().length < 3) return;
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(async () => {
      lookup = 'searching';
      const res = await geocode(a);
      if (res.status !== 'found') {
        lookup = res.status; // 'none' | 'unreachable': tell the user, keep the pin
        return;
      }
      lookup = 'found';
      lat = +res.value.lat.toFixed(6);
      lng = +res.value.lng.toFixed(6);
      placeMarker(res.value.lat, res.value.lng);
    }, 700);
  });

  onMount(async () => {
    L = (await import('leaflet')).default;
    const hasPos = !!(lat || lng);
    map = L.map(el, { zoomControl: true, attributionControl: true }).setView(
      hasPos ? [lat, lng] : [50.5, 9.5],
      hasPos ? 14 : 5
    );
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution: '© OpenStreetMap'
    }).addTo(map);
    if (hasPos) placeMarker(lat, lng, false);
    map.on('click', (e: Leaflet.LeafletMouseEvent) => {
      placeMarker(e.latlng.lat, e.latlng.lng, false);
      pinMoved(e.latlng.lat, e.latlng.lng);
    });
    setTimeout(() => map?.invalidateSize(), 60);
  });

  onDestroy(() => {
    clearTimeout(debounceTimer);
    map?.remove();
    map = null;
  });
</script>

<div class="picker">
  <div class="map" bind:this={el}></div>
  {#if lookup === 'searching'}
    <p class="hint busy"><span class="spin"></span> {$_('geo.searching')}</p>
  {:else if lookup === 'none'}
    <p class="hint warn">{$_('geo.not_found')}</p>
  {:else if lookup === 'unreachable'}
    <p class="hint warn">{$_('geo.unreachable')}</p>
  {:else if lookup === 'found' && (lat || lng)}
    <p class="hint ok">
      <Check size={13} aria-hidden="true" />
      {$_('geo.found', { values: { pos: fmtCoords(lat, lng) } })}
    </p>
  {:else}
    <p class="hint">{$_('apiaries.map_hint')}</p>
  {/if}
</div>

<style>
  .map {
    height: 240px;
    width: 100%;
    border-radius: 12px;
    overflow: hidden;
    border: 1px solid var(--line);
    z-index: 0;
  }
  .hint {
    color: var(--ink-soft);
    font-size: 0.8rem;
    margin: 6px 2px 0;
  }
  .hint.warn {
    color: #a8562e;
  }
  .hint.ok {
    color: #41502f;
  }
  .hint.busy,
  .hint.ok {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .spin {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    flex: none;
    border: 2px solid var(--line);
    border-top-color: var(--honey, #c77f22);
    animation: obh-spin 0.7s linear infinite;
  }
  @keyframes obh-spin {
    to {
      transform: rotate(360deg);
    }
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
