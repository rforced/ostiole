import js from '@eslint/js'
import { defineConfig } from 'eslint/config'
import skipFormatting from '@vue/eslint-config-prettier/skip-formatting'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'

export default defineConfig(
  {
    name: 'app/files-to-lint',
    files: ['**/*.{js,mjs,vue}'],
  },
  {
    name: 'app/files-to-ignore',
    ignores: ['**/dist/**', '**/coverage/**', '**/node_modules/**'],
  },
  js.configs.recommended,
  pluginVue.configs['flat/recommended'],
  {
    name: 'app/browser-globals',
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: { ...globals.browser },
    },
  },
  {
    // A component used in a template but never imported compiles, builds
    // and renders as an unknown element that silently drops its slots, so
    // lint is the only thing that catches it.
    name: 'app/undefined-components',
    files: ['src/**/*.vue'],
    rules: {
      'vue/no-undef-components': [
        'error',
        { ignorePatterns: ['Router(Link|View)', 'Tabs.*', 'Collapsible.*', 'Dialog.*'] },
      ],
    },
  },
  {
    name: 'app/node-files',
    files: ['vite.config.js', 'eslint.config.js', 'playwright.config.js', 'e2e/**/*.js'],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
  skipFormatting,
)
