import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { publicAssetsPlugin } from './build/public-assets.mjs'

export default defineConfig({
  plugins: [vue(), publicAssetsPlugin()],
  server: {
    host: '127.0.0.1',
    strictPort: true,
    fs: {
      strict: true,
    },
  },
  preview: {
    host: '127.0.0.1',
    strictPort: true,
  },
  build: {
    target: 'es2019',
  },
})
