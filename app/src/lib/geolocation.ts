// Whether the page may ask for the device's position at all.
//
// A server or proxy that sends `Permissions-Policy: geolocation=()` forbids
// it for the whole page: every request then fails at once, without asking the
// user, and a "Use my location" button could only ever show an error. Chromium
// tells the page about the policy up front (document.permissionsPolicy, and
// the older document.featurePolicy); other browsers don't, so there the button
// stays and a refused request is reported as before.

type Policy = { allowsFeature(feature: string): boolean };
type PolicyDocument = { permissionsPolicy?: Policy; featurePolicy?: Policy };

export function canAskForLocation(
  nav: Pick<Navigator, 'geolocation'> | undefined = globalThis.navigator,
  doc: PolicyDocument | undefined = globalThis.document as PolicyDocument | undefined
): boolean {
  if (!nav?.geolocation) return false;
  const policy = doc?.permissionsPolicy ?? doc?.featurePolicy;
  if (!policy) return true;
  try {
    return policy.allowsFeature('geolocation');
  } catch {
    return true; // unknown to this browser: let the request decide
  }
}
