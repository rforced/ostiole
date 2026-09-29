/**
 * The values a reading's row in the drives' history shows, as it shows
 * them: what the router searches too (smart.Reading.Search).
 * @param {object} r one reading
 * @returns {Array<string | undefined>}
 */
export function readingValues(r) {
  return [
    r.drive,
    r.model,
    r.serial,
    r.health,
    r.temperature != null ? `${r.temperature} °C` : undefined,
  ]
}
