<script lang="ts">
  import 'leaflet/dist/leaflet.css';
  import '#lib/map.css';
  import { onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { geocode, reverseGeocode, fmtCoords } from '#lib/geocode.js';
  import { getMapConfig, type MapConfig } from '#lib/instance.js';
  import { createMap, pinIcon, type LeafletLib } from '#lib/map.js';
  import { online } from '#lib/online.js';
  import { Check } from '@lucide/svelte';
  import type * as Leaflet from 'leaflet';

  // Two-way bound: the parent's address input and the pin stay in sync.
  // Typing an address geocodes it and moves the pin; moving the pin (drag or
  // map click) reverse-geocodes and rewrites the address, or falls back to
  // plain GPS coordinates when no address is known for the spot.
  //
  // Without a map (switched off by the operator, or offline) the position is
  // typed as latitude / longitude instead. A parent that has its own
  // coordinate fields passes coordInputs={false}.
  let {
    lat = $bindable(0),
    lng = $bindable(0),
    address = $bindable(''),
    coordInputs = true
  }: { lat?: number; lng?: number; address?: string; coordInputs?: boolean } = $props();

  // Unknown until the server's map settings (or the remembered ones) are in.
  let config = $state<MapConfig | null>(null);
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

  const showMap = $derived(!!config?.enabled && $online);
  const geocoder = $derived(config?.enabled ? config.geocoderUrl : '');

  // Asked again when the connection returns, in case the first try failed.
  $effect(() => {
    $online;
    void getMapConfig().then((c) => (config = c));
  });

  function placeMarker(a: number, b: number, pan = true) {
    if (!map || !L) return;
    if (!marker) {
      const m = L.marker([a, b], {
        icon: pinIcon(L),
        draggable: true,
        title: get(_)('geo.pin')
      }).addTo(map);
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
    if (!geocoder) {
      lookup = 'found'; // no address lookup configured: the position is all there is
      return;
    }
    lookup = 'searching';
    const res = await reverseGeocode(lat, lng, geocoder);
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
    const service = geocoder;
    if (!map || !service || a === selfWritten || a.trim().length < 3) return;
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(async () => {
      lookup = 'searching';
      const res = await geocode(a, service);
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

  // Builds the map when its container is shown and removes it when the
  // container goes away (offline), so Leaflet and the tiles load only then.
  function mount(el: HTMLDivElement) {
    let gone = false;
    const t = get(_);
    const hasPos = !!(lat || lng);
    if (config) {
      void createMap(el, config, {
        center: hasPos ? [lat, lng] : [50.5, 9.5],
        zoom: hasPos ? 14 : 5,
        zoomIn: t('geo.zoom_in'),
        zoomOut: t('geo.zoom_out')
      }).then((created) => {
        if (gone) return created.map.remove();
        L = created.L;
        map = created.map;
        if (lat || lng) placeMarker(lat, lng, false);
        created.map.on('click', (e: Leaflet.LeafletMouseEvent) => {
          placeMarker(e.latlng.lat, e.latlng.lng, false);
          pinMoved(e.latlng.lat, e.latlng.lng);
        });
      });
    }
    return {
      destroy() {
        gone = true;
        map?.remove();
        map = null;
        marker = null;
      }
    };
  }

  onDestroy(() => clearTimeout(debounceTimer));
</script>

<div class="picker">
  {#if showMap}
    <div class="map" use:mount></div>
    {#if lookup === 'searching'}
      <p class="hint busy"><span class="spin" aria-hidden="true"></span> {$_('geo.searching')}</p>
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
      <p class="hint">{$_(geocoder ? 'apiaries.map_hint' : 'geo.pin_hint')}</p>
    {/if}
  {:else if config}
    {#if config.enabled}
      <p class="placeholder">{$_('geo.map_offline')}</p>
    {/if}
    {#if coordInputs}
      <div class="coords">
        <label>
          <span>{$_('apiaries.lat')}</span>
          <input
            type="number"
            inputmode="decimal"
            step="0.000001"
            min="-90"
            max="90"
            bind:value={lat}
          />
        </label>
        <label>
          <span>{$_('apiaries.lng')}</span>
          <input
            type="number"
            inputmode="decimal"
            step="0.000001"
            min="-180"
            max="180"
            bind:value={lng}
          />
        </label>
      </div>
    {/if}
  {/if}
</div>

<style>
  .map {
    height: 240px;
    width: 100%;
    border-radius: 12px;
    overflow: hidden;
    border: 1px solid var(--border);
    z-index: 0;
  }
  .hint {
    color: var(--text-soft);
    font-size: 0.8rem;
    margin: 6px 2px 0;
  }
  .hint.warn {
    color: var(--danger);
  }
  .hint.ok {
    color: var(--ok);
  }
  .hint.busy,
  .hint.ok {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .placeholder {
    color: var(--text-soft);
    font-size: 0.85rem;
    text-align: center;
    border: 1px dashed var(--border-strong);
    border-radius: 12px;
    padding: 18px 14px;
  }
  .coords {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
    margin-top: 8px;
  }
  .coords label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .coords span {
    color: var(--text-soft);
    font-size: 0.8rem;
    font-weight: 600;
  }
  .coords input {
    font: inherit;
    font-size: 1rem;
    width: 100%;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--bg);
    color: var(--text);
  }
  .spin {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    flex: none;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    animation: obh-spin 0.7s linear infinite;
  }
  @keyframes obh-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .spin {
      animation: none;
    }
  }
</style>
