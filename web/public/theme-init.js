// Runs synchronously in <head> so the correct theme is applied before first paint.
// Must stay in sync with src/stores/theme.js (storage key, semantics and the
// theme-color values).
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
    var meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', dark ? '#171717' : '#ffffff')
  } catch {
    /* storage unavailable: fall through to system default */
  }
})()
