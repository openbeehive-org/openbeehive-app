// Whether this session is the shared, read-only demo account. Set by the app
// layout once it knows who is signed in; forms use it to say up front that
// nothing will be saved instead of letting a visitor fill them in for nothing.
import { writable } from 'svelte/store';

export const demoSession = writable(false);
