import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
// Where to proxy /api during development. Defaults to a backend running on the
// host; set BACKEND_URL when the backend lives in another container.
var target = process.env.BACKEND_URL || "http://localhost:8080";
export default defineConfig({
    plugins: [react()],
    server: {
        host: true, // listen on 0.0.0.0 so a forwarded container port works
        port: 5173,
        // Filesystem events are unreliable on mounted volumes, so poll instead.
        watch: { usePolling: true },
        proxy: {
            "/api": {
                target: target,
                changeOrigin: true,
            },
        },
    },
});
