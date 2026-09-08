# DevOps Tools UI

React + TypeScript single-page application, built with Vite.

```bash
npm install
npm run dev     # dev server on :5173, proxies /api to localhost:8080
npm run build   # production bundle into dist/
```

In production the bundle is embedded into the Go binary (see the root
`Dockerfile`), so there is no separate web server to deploy.
