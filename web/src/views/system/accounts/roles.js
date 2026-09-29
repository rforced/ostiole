/**
 * What an account or a token may do, written once. The noun is how the
 * role reads in the sentence a confirm dialog asks.
 */
export const ROLES = [
  { value: 'admin', label: 'Admin: everything, including accounts and updates', noun: 'an admin' },
  { value: 'operator', label: 'Operator: change and apply the configuration', noun: 'an operator' },
  { value: 'viewer', label: 'Viewer: read only', noun: 'a viewer' },
]
