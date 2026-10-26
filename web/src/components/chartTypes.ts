/** A time in seconds, then one value per column; a missing value is null or undefined. */
export type ChartPoint = (number | null | undefined)[]

/** A line: the column it reads, and optionally a low to high band and a limit. */
export interface ChartSeries {
  index: number
  name: string
  color: string
  band?: [low: number, high: number]
  limit?: number
}

/** A bar: the column it reads, a share from 0 to 100. */
export interface ChartBar {
  index: number
  name: string
  color: string
}

/** A value axis: its highest tick and the distance between ticks. */
export interface ChartScale {
  top: number
  step: number
}
