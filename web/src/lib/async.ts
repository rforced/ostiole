import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

/** The message of anything a request can throw. */
export function errorMessage(e: unknown): string {
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
 * overnight neither hammers the router nor shows it stale.
 *
 * Options: interval is the milliseconds between polls, 0 for none;
 * immediate runs once on mount; autostart (default) begins polling on
 * mount, and false leaves it to start().
 */
export function useAsync<T, A extends unknown[] = []>(
  fn: (...args: A) => Promise<T>,
  {
    interval = 0,
    immediate = false,
    autostart = true,
  }: { interval?: number; immediate?: boolean; autostart?: boolean } = {},
) {
  const busy = ref(false)
  const error = ref('')
  const updatedAt = ref(0)
  let inflight: Promise<T | undefined> | null = null
  /** Arguments of a run asked for while one was in flight. */
  let queued: A | null = null
  let timer = 0

  function run(...args: A): Promise<T | undefined> {
    if (inflight) {
      queued = args
      return inflight
    }
    busy.value = true
    inflight = (async () => {
      try {
        // Called inside a promise: a loader that throws before it awaits
        // would otherwise reach the finally before inflight is set.
        const out = await new Promise<T>((resolve) => resolve(fn(...args)))
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
          void run(...next)
        }
      }
    })()
    return inflight
  }

  /** Polls and the first read run the loader without arguments. */
  const bare = () => run(...([] as unknown as A))

  function start() {
    if (!interval || timer) return
    timer = window.setInterval(() => {
      if (document.visibilityState !== 'hidden') void bare()
    }, interval)
  }

  function stop() {
    if (timer) window.clearInterval(timer)
    timer = 0
  }

  function shown() {
    if (document.visibilityState === 'visible' && timer) void bare()
  }

  if (interval || immediate) {
    onMounted(() => {
      if (immediate) void bare()
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

/** What useAsync returns, for a prop that passes it down. */
export type AsyncLoad = ReturnType<typeof useAsync<unknown, unknown[]>>

/**
 * What an empty list says: "Reading…" until its first read lands, so it
 * never states a negative fact before it has looked, then `empty`. A read
 * that failed has landed too: its error says why the list is empty, and
 * "Reading…" would claim a read that is not happening. `load` is what
 * useAsync or useLog returned.
 */
export function emptyText(
  load: { updatedAt: Ref<number>; error?: Ref<string> },
  empty: string,
): string {
  return load.updatedAt.value || load.error?.value ? empty : 'Reading…'
}
