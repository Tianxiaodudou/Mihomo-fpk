import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产物直接进入 Go 后端的 embed 目录，随二进制一起分发（全离线，无外部 CDN）
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: '../src/static',
    emptyOutDir: true,
    target: 'es2018',
    assetsInlineLimit: 4096,
    chunkSizeWarningLimit: 2048
  },
  server: { port: 5273 }
})
