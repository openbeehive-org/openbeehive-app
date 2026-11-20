import { describe, it, expect, vi } from 'vitest';
import { createMap, pinIcon, popupHtml, type LeafletLib } from './map';

describe('popupHtml', () => {
  it('renders the name as text, never as markup', () => {
    const html = popupHtml('<img src=x onerror="alert(1)">');
    expect(html).toBe('<strong>&lt;img src=x onerror=&quot;alert(1)&quot;&gt;</strong>');
    expect(html).not.toContain('<img');
  });

  it('keeps ordinary names readable', () => {
    expect(popupHtml('Obstwiese & Wald')).toBe('<strong>Obstwiese &amp; Wald</strong>');
  });
});

// A stand-in for Leaflet that records how the map is set up.
const leaflet = vi.hoisted(() => {
  const map = { setView: vi.fn(), invalidateSize: vi.fn() };
  map.setView.mockReturnValue(map);
  const added = { addTo: vi.fn() };
  return {
    map,
    lib: {
      map: vi.fn(() => map),
      control: { zoom: vi.fn(() => added) },
      tileLayer: vi.fn((_url: string, _options: unknown) => added),
      divIcon: vi.fn((options: unknown) => options)
    }
  };
});
vi.mock('leaflet', () => ({ default: leaflet.lib }));

describe('createMap', () => {
  it('draws tiles from the configured server and names the zoom buttons', async () => {
    const el = {} as HTMLElement;
    const { map } = await createMap(
      el,
      { enabled: true, tileUrl: 'https://tiles.example.org/{z}/{x}/{y}.png', geocoderUrl: '' },
      { center: [50, 8], zoom: 7, zoomIn: 'Vergrößern', zoomOut: 'Verkleinern' }
    );
    expect(map).toBe(leaflet.map);
    expect(leaflet.lib.map).toHaveBeenCalledWith(el, { zoomControl: false });
    expect(leaflet.map.setView).toHaveBeenCalledWith([50, 8], 7);
    expect(leaflet.lib.tileLayer.mock.calls[0]?.[0]).toBe(
      'https://tiles.example.org/{z}/{x}/{y}.png'
    );
    expect(leaflet.lib.control.zoom).toHaveBeenCalledWith({
      zoomInTitle: 'Vergrößern',
      zoomOutTitle: 'Verkleinern'
    });
  });
});

describe('pinIcon', () => {
  it('is a CSS-drawn pin without user content', () => {
    const icon = pinIcon(leaflet.lib as unknown as LeafletLib) as unknown as Record<
      string,
      unknown
    >;
    expect(icon.className).toBe('obh-pin');
    expect(icon.html).toBe('<span></span>');
  });
});
