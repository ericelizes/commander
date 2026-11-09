import { defineConfig } from 'electron-vite'
import { rendererConfig } from './renderer.config'

export default defineConfig(({ command }) => ({
  main: {
    build: { outDir: 'out/main' },
  },
  preload: {
    build: { outDir: 'out/preload' },
  },
  renderer: rendererConfig(command),
}))
