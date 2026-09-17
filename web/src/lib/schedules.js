/**
 * The schedules people actually want, so nobody has to remember the cron
 * field order to get a nightly backup or a weekly patch run. Shared by
 * the cron dialog and the update settings, which offer the same list.
 *
 * @type {{value: string, label: string}[]}
 */
export const SCHEDULE_PRESETS = [
  { value: '0 4 * * *', label: 'Every night at 04:00' },
  { value: '0 * * * *', label: 'Every hour' },
  { value: '*/15 * * * *', label: 'Every 15 minutes' },
  { value: '0 4 * * 0', label: 'Sunday at 04:00' },
  { value: '0 4 1 * *', label: 'The first of the month at 04:00' },
]

/** The preset matching an expression, or "" when it is written by hand. */
export const presetFor = (schedule) =>
  SCHEDULE_PRESETS.some((p) => p.value === schedule) ? schedule : ''
