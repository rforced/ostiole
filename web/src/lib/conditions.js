/**
 * The conditions an alias keeps part of a JSON list by: "region=us-ashburn-1"
 * for a field and a value, or a bare key such as "hooks". The router ignores
 * case when it matches them, so two spellings of one condition are one.
 */

/**
 * Reads one condition as the router does.
 *
 * @param {string} text
 * @returns {{ field: string, value: string } | null} null when it is not one
 */
export function parseCondition(text) {
  const s = String(text ?? '').trim()
  if (!s || s.length > 128) return null
  const at = s.indexOf('=')
  if (at === -1) return { field: '', value: s }
  const field = s.slice(0, at).trim()
  const value = s.slice(at + 1).trim()
  if (!field || !value || field.includes('*')) return null
  return { field, value }
}

/**
 * The condition that keeps what sits under one value of a field, or under
 * one key when there is no field.
 *
 * @param {string} field
 * @param {string} value
 * @returns {string}
 */
export function formatCondition(field, value) {
  return field ? `${field}=${value}` : value
}

/**
 * What two spellings of one condition have in common.
 *
 * @param {string} text
 * @returns {string}
 */
export function conditionKey(text) {
  const c = parseCondition(text)
  return (c ? formatCondition(c.field, c.value) : String(text ?? '')).toLowerCase()
}
