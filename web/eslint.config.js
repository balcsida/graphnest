import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['../internal/webui/dist', 'node_modules']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, tseslint.configs.recommended, reactHooks.configs.flat.recommended, reactRefresh.configs.vite],
    languageOptions: { ecmaVersion: 2023, globals: globals.browser },
  },
  {
    // shadcn output: variants next to components and a media-query hook that sets state in an effect.
    files: ['src/components/ui/**', 'src/hooks/**'],
    rules: { 'react-refresh/only-export-components': 'off', 'react-hooks/set-state-in-effect': 'off', 'react-hooks/purity': 'off' },
  },
])
