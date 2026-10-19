<script lang="ts">
  // In-app scanner. Uses the native BarcodeDetector when available
  // (Android/Chrome). On platforms without it (e.g. iOS Safari) we tell the
  // user to use the phone's camera app; the QR is a normal URL and opens the
  // app the same way. A library like @zxing/browser can be dropped in here.
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import { parseHiveId } from '#lib/qr.js';

  // Minimal typing of the Barcode Detection API, which lib.dom does not ship yet.
  type BarcodeDetectorLike = { detect(source: ImageBitmapSource): Promise<{ rawValue: string }[]> };
  type BarcodeDetectorCtor = new (opts: { formats: string[] }) => BarcodeDetectorLike;
  const win = window as Window & { BarcodeDetector?: BarcodeDetectorCtor };

  let video: HTMLVideoElement;
  let stream: MediaStream | null = null;
  let raf = 0;
  let status = $state<'starting' | 'scanning' | 'unsupported' | 'denied'>('starting');

  onMount(async () => {
    const Detector = win.BarcodeDetector;
    if (!Detector) {
      status = 'unsupported';
      return;
    }
    try {
      stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
      video.srcObject = stream;
      await video.play();
      const detector = new Detector({ formats: ['qr_code'] });
      status = 'scanning';
      const tick = async () => {
        try {
          const codes = await detector.detect(video);
          for (const c of codes) {
            const id = parseHiveId(c.rawValue);
            if (id) {
              stop();
              await goto(`/hives/${id}`);
              return;
            }
          }
        } catch {
          /* frame skipped */
        }
        raf = requestAnimationFrame(tick);
      };
      raf = requestAnimationFrame(tick);
    } catch {
      status = 'denied';
    }
  });

  function stop() {
    cancelAnimationFrame(raf);
    stream?.getTracks().forEach((t) => t.stop());
    stream = null;
  }
  onDestroy(stop);
</script>

<div class="scan">
  <video bind:this={video} playsinline muted class:hidden={status !== 'scanning'}></video>

  {#if status === 'scanning'}
    <div class="frame"></div>
    <p class="hint">{$_('scan.aim')}</p>
  {:else if status === 'starting'}
    <p class="hint">{$_('scan.starting')}</p>
  {:else if status === 'denied'}
    <p class="hint">{$_('scan.denied')}</p>
  {:else}
    <div class="msg">
      <h2>{$_('scan.unsupported_title')}</h2>
      <p>{$_('scan.unsupported_text')}</p>
    </div>
  {/if}
</div>

<style>
  .scan {
    position: relative;
    min-height: 100vh;
    background: #000;
    display: grid;
    place-items: center;
    overflow: hidden;
  }
  video {
    width: 100%;
    height: 100%;
    object-fit: cover;
    position: absolute;
    inset: 0;
  }
  video.hidden {
    display: none;
  }
  .frame {
    position: relative;
    z-index: 2;
    width: 64vw;
    max-width: 280px;
    aspect-ratio: 1;
    border: 3px solid rgba(255, 255, 255, 0.9);
    border-radius: 22px;
    box-shadow: 0 0 0 100vmax rgba(0, 0, 0, 0.45);
  }
  .hint {
    position: absolute;
    bottom: 96px;
    z-index: 3;
    color: #fff;
    font-weight: 600;
    background: rgba(0, 0, 0, 0.5);
    padding: 8px 14px;
    border-radius: 999px;
  }
  .msg {
    z-index: 3;
    color: #fff;
    text-align: center;
    padding: 26px;
    max-width: 38ch;
  }
  .msg h2 {
    font-family: 'Fraunces', serif;
    margin-bottom: 10px;
  }
  .msg p {
    color: #ddd;
    line-height: 1.5;
  }
</style>
