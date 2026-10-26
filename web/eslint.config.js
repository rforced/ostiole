import js from '@eslint/js'
import { defineConfig } from 'eslint/config'
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
  {
    // The fixtures give each file its own server; a spec on Playwright's
    // own test would have none.
    name: 'app/e2e-fixtures',
    files: ['e2e/**/*.spec.js'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          paths: [
            { name: '@playwright/test', message: 'Import test and expect from ./fixtures.js.' },
          ],
        },
      ],
    },
  },
  {
    // bun fmt owns layout; these rules would fight it.
    name: 'app/skip-formatting',
    rules: {
      'no-unexpected-multiline': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/html-closing-bracket-spacing': 'off',
      'vue/html-end-tags': 'off',
      'vue/html-indent': 'off',
      'vue/html-quotes': 'off',
      'vue/html-self-closing': 'off',
      'vue/max-attributes-per-line': 'off',
      'vue/multiline-html-element-content-newline': 'off',
      'vue/mustache-interpolation-spacing': 'off',
      'vue/no-multi-spaces': 'off',
      'vue/no-spaces-around-equal-signs-in-attribute': 'off',
      'vue/singleline-html-element-content-newline': 'off',
    },
  },
)
