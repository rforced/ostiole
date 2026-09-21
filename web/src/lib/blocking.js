/** What a blocking lookup comes to, in one sentence. */

/**
 * @param {{name?: string, matched?: string, reason?: string} | null} finding
 * @returns {string}
 */
export function sentence(finding) {
  const f = finding
  if (!f) return ''
  switch (f.reason) {
    case 'not-a-name':
      return 'That is not a domain name.'
    case 'allow':
      return `Not blocked: the allow list has ${f.matched}.`
    case 'never':
      return `Not blocked: this router answers for ${f.matched} itself.`
    case 'delegated':
      return `Not blocked: ${f.matched} is a domain override, answered by its own resolvers.`
    case 'off':
      return 'Not blocked: the DNS server is off.'
    case 'lists-off':
      return 'Not blocked: block lists are off.'
    case 'deny':
      return `Blocked because the deny list has ${f.matched}.`
    case 'canary':
      return 'Blocked: Firefox asks this name before turning on DNS over HTTPS.'
    case 'list':
      return f.matched === f.name
        ? 'Blocked: a list has it.'
        : `Blocked: a list has ${f.matched}, which covers it.`
    default:
      return 'Not blocked: no list has it.'
  }
}
