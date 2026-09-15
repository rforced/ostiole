import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { THEME_STORAGE_KEY, useThemeStore } from '@/stores/theme'

function mockMatchMedia(dark) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: vi.fn().mockImplementation((query) => ({
      matches: dark,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

describe('theme store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.documentElement.classList.remove('dark')
  })

  it('defaults to system and follows a dark OS', () => {
    mockMatchMedia(true)
    const theme = useThemeStore()
    theme.init()
    expect(theme.preference).toBe('system')
    expect(theme.resolved).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('defaults to system and follows a light OS', () => {
    mockMatchMedia(false)
    const theme = useThemeStore()
    theme.init()
    expect(theme.resolved).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('persists an explicit choice and applies it', async () => {
    mockMatchMedia(false)
    const theme = useThemeStore()
    theme.init()
    theme.set('dark')
    await Promise.resolve()
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark')
    expect(theme.resolved).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('clears storage when returning to system', () => {
    mockMatchMedia(true)
    localStorage.setItem(THEME_STORAGE_KEY, 'light')
    const theme = useThemeStore()
    expect(theme.preference).toBe('light')
    theme.set('system')
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull()
    expect(theme.resolved).toBe('dark')
  })
})
