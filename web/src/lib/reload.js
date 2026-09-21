/**
 * Recovery for a browser left running a build the router no longer serves.
 *
 * Vite content-hashes every chunk, and an Ostiole update replaces the lot.
 * A page still running the old build that navigates to a route it has not
 * visited yet asks for a chunk the new binary has never heard of. The SPA
 * fallback answers that with index.html, so the import fails on the MIME
 * type rather than on a 404, and the navigation dies with nothing on
 * screen to explain it. Reloading picks up the build that is actually
 * serving.
 */

/** Where the last automatic reload is remembered, so one fault cannot loop. */
export const RELOAD_KEY = 'ostiole.chunkReload'

/**
 * How long a second preload failure is treated as the same one. A stale
 * chunk is cured by a single reload; anything still failing after that is
 * a real fault, and a reload loop would only hide it.
 */
export const RELOAD_WINDOW_MS = 10_000

function reloadedRecently() {
  try {
    const last = Number(sessionStorage.getItem(RELOAD_KEY)) || 0
    return Date.now() - last < RELOAD_WINDOW_MS
  } catch {
    // Storage unavailable: the in-page guard is all there is.
    return false
  }
}

function rememberReload() {
  try {
    sessionStorage.setItem(RELOAD_KEY, String(Date.now()))
  } catch {
    /* storage unavailable */
  }
}

/**
 * Reload once when a lazy route chunk cannot be loaded.
 *
 * @param {EventTarget} [target] where the event lands; the window in the app.
 * @param {() => void} [reload] how to reload; injected so a test can watch it.
 */
export function installChunkReload(target = window, reload = () => window.location.reload()) {
  let reloading = false

  target.addEventListener('vite:preloadError', (event) => {
    // Several chunks can fail at once; the first one is the only one that
    // needs answering.
    if (reloading || reloadedRecently()) return
    reloading = true
    rememberReload()
    // Without this Vite rethrows, and the router reports a navigation
    // failure that the reload is about to make irrelevant.
    event.preventDefault()
    reload()
  })
}
