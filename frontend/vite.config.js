import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { publicAssetsPlugin } from './build/public-assets.mjs'
import { ossInventoryPlugin } from './build/oss-inventory.mjs'
import { ossCatalogPlugin, resolveDisplayTarget } from './build/oss-catalog.mjs'

export default defineConfig({
  define: { __OMNI_DESKTOP_BUILD__: JSON.stringify(resolveDisplayTarget(process.env).startsWith('desktop-')) },
  plugins: [{ name: 'wails-bundled-runtime', enforce: 'pre',
    resolveId(id) { if (id === '/wails/runtime.js') return { id, external: true } },
  }, vue(), ossCatalogPlugin(), ossInventoryPlugin(), publicAssetsPlugin()],
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 5173,
    ...(process.env.OMNI_WAILS_BROWSER_PORT ? { hmr: { path: '/__vite_hmr', clientPort: Number(process.env.OMNI_WAILS_BROWSER_PORT) } } : {}),
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
    rollupOptions: { external: ['/wails/runtime.js'] },
  },
})
