export interface CronStatus {
  id: string
  origin: 'system' | 'user'
  description: string
  schedule: string
  enabled: boolean
  kind?: string
  next?: string
  lastRun?: string
  lastError?: string
  lastOutput?: string
  lastSeconds?: number
  running: boolean
}

export interface CronRun {
  error?: string
}
