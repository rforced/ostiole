import type { Reading } from '@/lib/types.gen'

/** One reading of a drive's health; the figures are null or absent when it gave none. */
export type DriveReading = Reading

/**
 * The values a reading's row in the drives' history shows, as it shows
 * them: what the router searches too (smart.Reading.Search).
 */
export function readingValues(r: DriveReading): (string | undefined)[] {
  return [
    r.drive,
    r.model,
    r.serial,
    r.health,
    r.temperature != null ? `${r.temperature} °C` : undefined,
  ]
}
