import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { ossCatalogPlugin } from './build/oss-catalog.mjs'

export default defineConfig({
  plugins: [vue(), ossCatalogPlugin()],
  test: {
    environment: 'jsdom',
    include: ['tests/component/**/*.test.js'],
    setupFiles: ['./tests/component/setup.js'],
    clearMocks: true,
    restoreMocks: true,
    unstubEnvs: true,
    unstubGlobals: true
  }
})
