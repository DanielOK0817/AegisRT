import { fileURLToPath, URL } from "node:url"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import { defineConfig } from "vite"

export default defineConfig({
  base: "/api/aegisrt/ui/",
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "../presentation",
    emptyOutDir: false,
    assetsDir: "",
    sourcemap: false,
    rollupOptions: {
      output: {
        entryFileNames: "runtime.js",
        chunkFileNames: "runtime-[name].js",
        assetFileNames: (asset) =>
          asset.names.some((name) => name.endsWith(".css")) ? "runtime.css" : "[name][extname]",
      },
    },
  },
})
