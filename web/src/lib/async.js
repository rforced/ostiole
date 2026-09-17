import { onBeforeUnmount, onMounted, ref } from 'vue'

/** The message of anything a request can throw. */
export function errorMessage(e) {
  return e instanceof Error ? e.message : String(e)
}

/**
 * The state every page used to keep by hand around a loader: a busy flag,
 * an error, when it last succeeded, and an optional poll.
 *
 * `run` never throws; a failure lands in `error` and resolves undefined.
 * A call while one is in flight shares that one and runs once more after
 * it, because the second request may be about something the first read
 * too early: an apply that just landed. A poll pauses while the tab is
 * hidden and runs once more when it is shown again, so a page left open
 * overnight neither hammers the box nor shows it stale.
 *
 * @template T
 * @param {(...args: any[]) => Promise<T>} fn
 * @param {{interval?: number, immediate?: boolean, autostart?: boolean}} [opts]
 *   interval: milliseconds between polls, 0 for none;
 *   immediate: run once on mount;
 *   autostart: begin polling on mount (default); false leaves it to start()
 */
export function useAsync(fn, { interval = 0, immediate = false, autostart = true } = {}) {
  const busy = ref(false)
  const error = ref('')
  const updatedAt = ref(0)
  /** @type {Promise<T | undefined> | null} */
  let inflight = null
  /** @type {any[] | null} arguments of a run asked for while one was in flight */
  let queued = null
  let timer = 0

  function run(...args) {
    if (inflight) {
      queued = args
      return inflight
    }
    busy.value = true
    inflight = (async () => {
      try {
        const out = await fn(...args)
        error.value = ''
        updatedAt.value = Date.now()
        return out
      } catch (e) {
        error.value = errorMessage(e)
        return undefined
      } finally {
        busy.value = false
        inflight = null
        if (queued) {
          const next = queued
          queued = null
          run(...next)
        }
      }
    })()
    return inflight
  }

  function start() {
    if (!interval || timer) return
    timer = window.setInterval(() => {
      if (document.visibilityState !== 'hidden') run()
    }, interval)
  }

  function stop() {
    if (timer) window.clearInterval(timer)
    timer = 0
  }

  function shown() {
    if (document.visibilityState === 'visible' && timer) run()
  }

  if (interval || immediate) {
    onMounted(() => {
      if (immediate) run()
      if (autostart) start()
      document.addEventListener('visibilitychange', shown)
    })
    onBeforeUnmount(() => {
      stop()
      document.removeEventListener('visibilitychange', shown)
    })
  }

  return { busy, error, updatedAt, run, start, stop }
}
