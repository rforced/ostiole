import type { ServiceState } from '@/lib/types.gen'

/** A unit's state in the service pages' words: off when the configuration leaves it off. */
export function stateWord(s: Pick<ServiceState, 'state' | 'want'>) {
  if (s.state === 'active') return 'running'
  if (s.state === 'inactive') return s.want ? 'stopped' : 'off'
  return s.state
}
