import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vitest/config'

// The Go binary embeds this directory (internal/web/embed.go).
const outDir = '../internal/web/dist'

// emptyOutDir removes the .gitkeep that keeps the embed directory present on a
// fresh clone; put it back after every build.
function keepGitkeep() {
  return {
    name: 'ostiole:keep-gitkeep',
    closeBundle() {
      writeFileSync(join(outDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [vue(), tailwindcss(), keepGitkeep()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir,
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
      },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.js'],
  },
})
