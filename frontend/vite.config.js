import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { publicAssetsPlugin } from './build/public-assets.mjs'
import { ossInventoryPlugin } from './build/oss-inventory.mjs'
import { ossCatalogPlugin } from './build/oss-catalog.mjs'

export default defineConfig({
  plugins: [vue(), ossCatalogPlugin(), ossInventoryPlugin(), publicAssetsPlugin()],
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
