/**
 * Who did what on the router, as the audit log and History show it. The
 * sentence for each entry comes from the router (audit.Event.Sentence).
 */

/**
 * The actor's name, or the router's own doing.
 * @param {{name?: string}} [actor]
 */
export function who(actor) {
  return actor?.name || 'Router'
}

/**
 * The actor named with what they were when not an account: "alice",
 * "deploy (token)", "carol (shell)", "Router".
 * @param {{name?: string, kind?: string}} [actor]
 */
export function actorText(actor) {
  const kind = actor?.kind
  return actor?.name && kind && kind !== 'account' ? `${actor.name} (${kind})` : who(actor)
}

/**
 * The line under the actor's name in the audit log: what they were when
 * not an account, their role and where they came from.
 * @param {{kind?: string, role?: string, address?: string}} [actor]
 */
export function actorParts(actor) {
  return [actor?.kind === 'account' ? '' : actor?.kind, actor?.role, actor?.address].filter(Boolean)
}

/**
 * What a row of the audit log shows, as the router searches it
 * (audit.Event.Search).
 * @param {{text?: string, by?: object}} e
 */
export function auditValues(e) {
  return [e.text, who(e.by), ...actorParts(e.by)]
}
