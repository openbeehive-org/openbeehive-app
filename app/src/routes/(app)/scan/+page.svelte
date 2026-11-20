<script lang="ts">
  // In-app scanner. It uses the browser's own BarcodeDetector where that
  // exists (Chrome on Android) and otherwise a bundled decoder, loaded on
  // demand (iPhone, Firefox). Scanning inside the app matters on iOS: the
  // system camera opens a label link in Safari, whose storage is separate
  // from the installed app's.
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import { cameraProblem, parseHiveId, type CameraProblem } from '#lib/qr.js';
  import { hives } from '#lib/local/repo.js';

  // Minimal typing of the Barcode Detection API, which lib.dom does not ship yet.
  type BarcodeDetectorLike = { detect(source: ImageBitmapSource): Promise<{ rawValue: string }[]> };
  type BarcodeDetectorCtor = (new (opts: { formats: string[] }) => BarcodeDetectorLike) & {
    getSupportedFormats?: () => Promise<string[]>;
  };
  const win = window as Window & { BarcodeDetector?: BarcodeDetectorCtor };

  let video = $state<HTMLVideoElement>();
  let status = $state<'starting' | 'scanning' | CameraProblem>('starting');
  // A code was read, but it is not a hive label.
  let foreignCode = $state(false);
  // Nothing was read for a while: the label may be worn or badly lit.
  let slow = $state(false);

  let code = $state('');
  let codeUnknown = $state(false);

  let stream: MediaStream | null = null;
  let raf = 0;
  let stopDecoder: (() => void) | null = null;
  let slowTimer: ReturnType<typeof setTimeout> | undefined;
  let foreignTimer: ReturnType<typeof setTimeout> | undefined;
  // Set when the page is left or a hive was found; late callbacks then do nothing.
  let finished = false;

  async function nativeDetector(): Promise<BarcodeDetectorLike | null> {
    const Detector = win.BarcodeDetector;
    if (!Detector) return null;
    try {
      const formats = await Detector.getSupportedFormats?.();
      if (formats && !formats.includes('qr_code')) return null;
      return new Detector({ formats: ['qr_code'] });
    } catch {
      return null;
    }
  }

  async function start() {
    finished = false;
    status = 'starting';
    foreignCode = slow = false;
    if (!navigator.mediaDevices?.getUserMedia || !video) {
      status = 'nocamera';
      return;
    }
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: { ideal: 'environment' } },
        audio: false
      });
    } catch (e) {
      status = cameraProblem(e);
      return;
    }
    if (finished) return release(); // the page was left while the browser asked for permission
    try {
      video.srcObject = stream;
      await video.play();
      const detector = await nativeDetector();
      if (detector) scanNatively(detector, video);
      else await scanWithLibrary(video);
    } catch {
      stop();
      status = 'failed';
      return;
    }
    if (finished) return stop();
    status = 'scanning';
    slowTimer = setTimeout(() => (slow = true), 12_000);
  }

  function scanNatively(detector: BarcodeDetectorLike, source: HTMLVideoElement) {
    const tick = async () => {
      if (finished) return;
      try {
        for (const c of await detector.detect(source)) onCode(c.rawValue);
      } catch {
        /* frame skipped */
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
  }

  // The decoder is a separate chunk: devices with a native detector never load it.
  async function scanWithLibrary(source: HTMLVideoElement) {
    const { BrowserQRCodeReader } = await import('@zxing/browser');
    const reader = new BrowserQRCodeReader(undefined, { delayBetweenScanAttempts: 120 });
    const controls = await reader.decodeFromVideoElement(source, (result) => {
      if (result) onCode(result.getText());
    });
    stopDecoder = () => controls.stop();
  }

  function onCode(text: string) {
    if (finished) return;
    const id = parseHiveId(text);
    if (!id) {
      foreignCode = true;
      clearTimeout(foreignTimer);
      foreignTimer = setTimeout(() => (foreignCode = false), 4000);
      return;
    }
    stop();
    // The label target looks the hive up, syncs if needed and explains a miss.
    void goto(`/h/${id}`);
  }

  // For a label the camera cannot read: the code printed under the QR.
  async function openByCode() {
    codeUnknown = false;
    const found = await hives.findByShortCode(code);
    const only = found.length === 1 ? found[0] : undefined;
    if (!only) {
      codeUnknown = true;
      return;
    }
    stop();
    await goto(`/hives/${only.id}`);
  }

  function release() {
    stream?.getTracks().forEach((t) => t.stop());
    stream = null;
    if (video) video.srcObject = null;
  }
  function stop() {
    finished = true;
    cancelAnimationFrame(raf);
    clearTimeout(slowTimer);
    clearTimeout(foreignTimer);
    stopDecoder?.();
    stopDecoder = null;
    release();
  }

  onMount(() => void start());
  onDestroy(stop);
</script>

<svelte:head><title>{$_('scan.title')}</title></svelte:head>

<div class="page">
  <h1>{$_('scan.title')}</h1>

  <div class="viewport" class:hidden={status !== 'scanning' && status !== 'starting'}>
    <video bind:this={video} playsinline muted class:hidden={status !== 'scanning'}></video>
    {#if status === 'scanning'}
      <div class="frame" aria-hidden="true"></div>
      <p class="hint" role="status">
        {foreignCode ? $_('scan.not_hive') : slow ? $_('scan.hard_to_read') : $_('scan.aim')}
      </p>
    {:else if status === 'starting'}
      <p class="hint" role="status">{$_('scan.starting')}</p>
    {/if}
  </div>

  {#if status === 'denied' || status === 'nocamera' || status === 'failed'}
    <div class="card msg" role="alert">
      <h2>{$_(`scan.${status}_title`)}</h2>
      <p>{$_(`scan.${status}_text`)}</p>
      <button class="primary" onclick={start}>{$_('scan.retry')}</button>
    </div>
  {/if}

  <form
    class="card code"
    onsubmit={(e) => {
      e.preventDefault();
      openByCode();
    }}
  >
    <label class="field">
      <span>{$_('scan.code_label')}</span>
      <input
        bind:value={code}
        placeholder={$_('scan.code_ph')}
        autocapitalize="characters"
        autocomplete="off"
        spellcheck="false"
        maxlength="12"
        aria-describedby="code-help"
      />
    </label>
    <button class="ghost" type="submit" disabled={code.trim().length < 6}
      >{$_('scan.code_open')}</button
    >
    <p id="code-help" class="help">{$_('scan.code_help')}</p>
    {#if codeUnknown}<p class="error" role="alert">{$_('scan.code_unknown')}</p>{/if}
  </form>
</div>

<style>
  .page {
    max-width: 720px;
    margin: 0 auto;
    padding: 26px 18px 96px;
    display: grid;
    gap: 16px;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
  }
  /* The camera view is dark with light text in every theme: it sits on live video. */
  .viewport {
    position: relative;
    height: min(62vh, 560px);
    min-height: 280px;
    border-radius: 18px;
    overflow: hidden;
    background: black;
    display: grid;
    place-items: center;
  }
  .hidden {
    display: none;
  }
  video {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .frame {
    position: relative;
    width: 64%;
    max-width: 280px;
    aspect-ratio: 1;
    border: 3px solid white;
    border-radius: 22px;
    box-shadow: 0 0 0 100vmax color-mix(in srgb, black 45%, transparent);
  }
  .hint {
    position: absolute;
    left: 12px;
    right: 12px;
    bottom: 14px;
    margin: 0 auto;
    width: fit-content;
    max-width: calc(100% - 24px);
    text-align: center;
    color: white;
    font-weight: 600;
    background: color-mix(in srgb, black 60%, transparent);
    padding: 8px 14px;
    border-radius: 14px;
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 16px;
  }
  .msg {
    display: grid;
    gap: 10px;
    justify-items: start;
  }
  .msg h2 {
    font-family: 'Fraunces', serif;
    font-size: 1.3rem;
  }
  .msg p,
  .help {
    color: var(--text-soft);
    line-height: 1.5;
  }
  .code {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: end;
    gap: 10px;
  }
  .help,
  .error {
    grid-column: 1 / -1;
    font-size: 0.85rem;
  }
  .error {
    color: var(--danger);
    font-weight: 600;
  }
  .field {
    display: grid;
    gap: 4px;
  }
  .field span {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .field input {
    font: inherit;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    width: 100%;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text);
  }
  .field input::placeholder {
    letter-spacing: normal;
    text-transform: none;
  }
  .primary,
  .ghost {
    min-height: 44px;
    border-radius: 11px;
    padding: 10px 16px;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
  }
  .primary {
    background: var(--accent);
    color: var(--on-accent);
    border: none;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--border-strong);
    color: var(--text);
  }
  .ghost:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>
