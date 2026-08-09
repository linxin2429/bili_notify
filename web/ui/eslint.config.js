import eslint from '@eslint/js'
import boundaries from 'eslint-plugin-boundaries'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

const sourceFiles = ['src/**/*.{ts,tsx}']
const typedFiles = ['**/*.{ts,tsx}']

export default tseslint.config(
  {
    ignores: [
      '../dist/**',
      'coverage/**',
      'node_modules/**',
      'playwright-report/**',
      'test-results/**',
    ],
  },
  eslint.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked.map(config => ({
    ...config,
    files: typedFiles,
  })),
  {
    files: typedFiles,
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    ...reactHooks.configs.flat['recommended-latest'],
    files: sourceFiles,
    languageOptions: {
      globals: globals.browser,
    },
  },
  {
    files: ['eslint.config.js', 'scripts/**/*.mjs', 'vite.config.ts', 'playwright.config.ts', 'test-workers.ts'],
    languageOptions: {
      globals: globals.node,
    },
  },
  {
    files: sourceFiles,
    plugins: {
      boundaries,
    },
    settings: {
      'import/resolver': {
        node: {
          extensions: ['.js', '.jsx', '.ts', '.tsx'],
        },
      },
      'boundaries/include': ['src/**/*'],
      'boundaries/elements': [
        { type: 'app', pattern: 'src/app' },
        { type: 'page', pattern: 'src/pages' },
        { type: 'module', pattern: 'src/modules/*' },
        { type: 'shared', pattern: 'src/shared' },
      ],
    },
    rules: {
      ...boundaries.configs.recommended.rules,
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-misused-promises': 'error',
      '@typescript-eslint/no-unnecessary-condition': 'off',
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-argument': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-return': 'off',
      '@typescript-eslint/require-await': 'off',
      'no-restricted-imports': [
        'error',
        {
          patterns: [{
            group: ['../pages/*', '../../pages/*', '**/modules/*/*'],
            message: '依赖必须遵守分层方向，跨模块导入必须经过模块公共入口',
          }],
        },
      ],
      'boundaries/dependencies': [
        'error',
        {
          default: 'disallow',
          policies: [
            {
              from: { element: { type: 'app' } },
              allow: { to: { element: { types: { anyOf: ['app', 'page', 'module', 'shared'] } } } },
            },
            {
              from: { element: { type: 'page' } },
              allow: { to: { element: { types: { anyOf: ['module', 'shared'] } } } },
            },
            {
              from: { element: { type: 'module' } },
              allow: { to: { element: { types: { anyOf: ['module', 'shared'] } } } },
            },
            {
              from: { element: { type: 'shared' } },
              allow: { to: { element: { type: 'shared' } } },
            },
          ],
        },
      ],
    },
  },
  {
    files: ['**/*.test.{ts,tsx}', 'src/test/**/*.{ts,tsx}', 'src/test-setup.ts'],
    rules: {
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-argument': 'off',
      '@typescript-eslint/no-unsafe-call': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-return': 'off',
    },
  },
)
