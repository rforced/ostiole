/** The longest raw agent shown when it is not recognised. */
const RAW_LIMIT = 40

// Order matters: Edge and Opera also say Chrome, Chrome also says Safari.
const BROWSERS = [
  [/\bEdg(?:e|A|iOS)?\//, 'Edge'],
  [/\b(?:OPR|Opera)\//, 'Opera'],
  [/\bSamsungBrowser\//, 'Samsung Internet'],
  [/\bVivaldi\//, 'Vivaldi'],
  [/\b(?:Firefox|FxiOS)\//, 'Firefox'],
  [/\b(?:Chrome|CriOS|Chromium)\//, 'Chrome'],
  [/\bVersion\/[\d.]+.*\bSafari\//, 'Safari'],
]

// iPhone and iPad also say "like Mac OS X", Android also says Linux.
const SYSTEMS = [
  [/\biPhone\b/, 'iPhone'],
  [/\biPad\b/, 'iPad'],
  [/\bAndroid\b/, 'Android'],
  [/\bWindows\b/, 'Windows'],
  [/\bCrOS\b/, 'ChromeOS'],
  [/\bMac OS X\b|\bMacintosh\b/, 'macOS'],
  [/\bLinux\b/, 'Linux'],
]

const match = (list, s) => list.find(([re]) => re.test(s))?.[1] ?? ''

/**
 * A browser's user agent in a few words: "Firefox on Linux". An agent it
 * cannot read is shown raw, cut short.
 */
export function describe(agent: string | null | undefined): string {
  const s = (agent ?? '').trim()
  if (!s) return 'unknown'
  const browser = match(BROWSERS, s)
  if (!browser) return s.length > RAW_LIMIT ? `${s.slice(0, RAW_LIMIT - 1)}…` : s
  const system = match(SYSTEMS, s)
  return system ? `${browser} on ${system}` : browser
}
