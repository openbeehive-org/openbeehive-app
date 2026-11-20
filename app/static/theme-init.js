// Sets the colour theme before the first paint, so a dark-mode user never sees
// a light flash. Loaded as a blocking same-origin script from app.html (an
// inline script would need its own Content-Security-Policy hash). The rule and
// the storage key mirror src/lib/theme.ts.
(function () {
  var choice = 'system';
  try {
    choice = localStorage.getItem('obh.theme') || 'system';
  } catch {
    // storage blocked: follow the system
  }
  var systemDark = !!window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  var dark = choice === 'dark' || (choice !== 'light' && systemDark);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
})();
