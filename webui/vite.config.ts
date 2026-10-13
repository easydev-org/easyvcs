import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'
import path from 'node:path'

// Embedded in the easyvcs server binary and served at /ui/ (see server/ui.go).
// The REST API is same-origin (/api/v1), so no base URL is needed.
export default defineConfig({
  base: '/ui/',
  plugins: [tailwindcss(), svelte()],
  resolve: { alias: { $lib: path.resolve('./src/lib') } },
  build: { outDir: '../server/ui/dist', emptyOutDir: true, target: 'es2022' },
})
