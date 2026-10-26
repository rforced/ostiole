/** Whether two WAF exclusions switch off the same thing: rule, path and variable. */
export function sameExclusion(a, b) {
  return (
    a.rule === b.rule && (a.path ?? '') === (b.path ?? '') && (a.target ?? '') === (b.target ?? '')
  )
}

/** An exclusion in words: "rule 942100 for ARGS:q on /search". */
export function exclusionText(e) {
  let out = `rule ${e.rule}`
  if (e.target) out += ` for ${e.target}`
  if (e.path) out += ` on ${e.path}`
  return out
}

/** The first rule ID of an exclusion, which a range sorts by. */
export function firstRule(e) {
  return Number.parseInt(e.rule, 10)
}
