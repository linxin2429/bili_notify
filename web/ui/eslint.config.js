import eslint from '@eslint/js'
import hooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['../dist/**', 'coverage/**'] },
  eslint.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  hooks.configs.flat['recommended-latest'],
  {
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: {
      globals: { ...globals.browser, ...globals.es2022 },
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-misused-promises': 'error',
      '@typescript-eslint/no-unnecessary-condition': 'off',
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-argument': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-return': 'off',
      '@typescript-eslint/require-await': 'off',
      'no-restricted-imports': ['error', { patterns: [{ group: ['../pages/*', '../../pages/*'], message: 'modules/shared 不得依赖 pages' }] }],
    },
  },
  { files: ['src/**/*.test.{ts,tsx}', 'src/test-setup.ts'], rules: { '@typescript-eslint/no-unsafe-call': 'off', '@typescript-eslint/no-unsafe-assignment': 'off' } },
)
