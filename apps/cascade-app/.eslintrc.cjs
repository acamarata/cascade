module.exports = {
  root: true,
  parser: '@typescript-eslint/parser',
  parserOptions: {
    ecmaVersion: 'latest',
    sourceType: 'module',
    ecmaFeatures: { jsx: true },
  },
  env: {
    browser: true,
    es2022: true,
    node: true,
  },
  plugins: ['@typescript-eslint', 'react', 'react-hooks', 'jsx-a11y'],
  extends: [
    'eslint:recommended',
    'plugin:@typescript-eslint/recommended',
    'plugin:react/recommended',
    'plugin:react-hooks/recommended',
    'plugin:jsx-a11y/recommended',
    'prettier', // must be last — disables conflicting stylistic rules
  ],
  settings: {
    react: { version: 'detect' },
  },
  rules: {
    // React 17+ JSX transform — no React import needed
    'react/react-in-jsx-scope': 'off',
    // TypeScript handles prop validation; react/prop-types is redundant with TS
    'react/prop-types': 'off',
    // Project standard: no-any is an error
    '@typescript-eslint/no-explicit-any': 'error',
    // Allow _-prefixed variables as intentionally unused (convention)
    '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    // Derived-state sync and debounce setup in effects are valid patterns used throughout this
    // codebase; the rule flags them as perf hazards but these are intentional and reviewed
    'react-hooks/set-state-in-effect': 'off',
  },
}
