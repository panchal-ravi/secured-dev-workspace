import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build is emitted into the Go backend's ./web dir so the portal binary
// serves the SPA from a single origin (matching the OIDC redirect to :8080).
// `npm run dev` proxies the API/auth routes to the backend for local hacking.
export default defineConfig({
  plugins: [react()],
  build: { outDir: '../backend/web', emptyOutDir: true },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
    },
  },
})
