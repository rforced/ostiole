/**
 * Who did what on the router, as the audit log and History show it. The
 * sentence for each entry comes from the router (audit.Event.Sentence).
 */

import type { AuditActor, AuditEvent } from '@/lib/api'

/** The actor's name, or the router's own doing. */
export function who(actor?: AuditActor): string {
  return actor?.name || 'Router'
}

/**
 * The actor named with what they were when not an account: "alice",
 * "deploy (token)", "carol (shell)", "Router".
 */
export function actorText(actor?: AuditActor): string {
  const kind = actor?.kind
  return actor?.name && kind && kind !== 'account' ? `${actor.name} (${kind})` : who(actor)
}

/**
 * The line under the actor's name in the audit log: what they were when
 * not an account, their role and where they came from.
 */
export function actorParts(actor?: AuditActor): string[] {
  return [actor?.kind === 'account' ? '' : actor?.kind, actor?.role, actor?.address].filter(
    (part): part is string => Boolean(part),
  )
}

/**
 * What a row of the audit log shows, as the router searches it
 * (audit.Event.Search).
 */
export function auditValues(e: AuditEvent): (string | undefined)[] {
  return [e.text, who(e.by), ...actorParts(e.by)]
}
