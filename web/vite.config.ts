import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const base = process.env.BASE_URL ?? '/Tollgate/'

// The shared brand bar references /techmines-logo.jpg from the site root. On GitHub
// Pages the app lives under /Tollgate/, so prefix the path at build time instead of
// editing the kit-managed component.
function brandAssetBase(): Plugin {
  return {
    name: 'tollgate-brand-asset-base',
    transform(code, id) {
      if (!id.includes('/components/brand/')) return
      return code.replaceAll('"/techmines-logo.jpg"', JSON.stringify(`${base}techmines-logo.jpg`))
    },
  }
}

export default defineConfig({
  base,
  plugins: [react(), tailwindcss(), brandAssetBase()],
  build: { target: 'es2022' },
  server: { port: 5180 },
})
