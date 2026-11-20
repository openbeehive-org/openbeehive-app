// Whether the browser believes it has a network connection.
import { readable } from 'svelte/store';

export const online = readable(true, (set) => {
  if (typeof window === 'undefined') return;
  const update = () => set(navigator.onLine);
  update();
  window.addEventListener('online', update);
  window.addEventListener('offline', update);
  return () => {
    window.removeEventListener('online', update);
    window.removeEventListener('offline', update);
  };
});
