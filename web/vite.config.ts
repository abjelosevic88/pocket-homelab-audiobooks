import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist', emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 700,
    rollupOptions: { input: { index: 'index.html', 'share-player': 'src/share-player.ts' }, output: { entryFileNames: chunk => chunk.name==='share-player' ? 'share-player.js' : 'assets/[name]-[hash].js', manualChunks: { react: ['react', 'react-dom', 'react-router-dom'] } } },
  },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080', '/healthz': 'http://localhost:8080' },
  },
})
