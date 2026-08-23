import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { appVersion } from './build-version.ts';

export default defineConfig({
  // Same define as vite.config.ts — without it `__APP_VERSION__` is undefined
  // under test. Tests must not assert the literal string: the patch number is
  // the commit count, so it changes with every commit.
  define: { __APP_VERSION__: JSON.stringify(appVersion()) },
  plugins: [react()],
  test: {
    include: ['src/**/*.test.{ts,tsx}'],
    environment: 'jsdom',
    globals: false,
    setupFiles: ['./src/test/setup.ts'],
  },
});
