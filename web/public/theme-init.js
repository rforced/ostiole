// Runs synchronously in <head> so the correct theme is applied before first paint.
// Must stay in sync with src/stores/theme.ts (storage key and semantics).
;(function () {
  try {
    var pref = localStorage.getItem('ostiole.theme')
    var dark =
      pref === 'dark' ||
      ((pref === null || pref === 'system') &&
        window.matchMedia('(prefers-color-scheme: dark)').matches)
    var root = document.documentElement
    root.classList.toggle('dark', dark)
    root.style.colorScheme = dark ? 'dark' : 'light'
  } catch {
    /* storage unavailable: fall through to system default */
  }
})()
