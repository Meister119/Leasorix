# web

Web-UI von Leasorix (Vite + React + TypeScript + Tailwind). Der API-Client
wird aus `../internal/api/openapi.yaml` generiert (`npm run gen`). `make build`
im Repo-Root baut die UI und bettet sie per `go:embed` (Build-Tag `ui`) ins
Binary ein.

```sh
npm ci
npm run dev        # Vite-Devserver, Proxy /api -> LEASORIX_URL (Default https://127.0.0.1:8067)
npm test           # Vitest
npm run typecheck
```

Die Playwright-E2E-Tests laufen über `make test-e2e` (root, Network Namespaces).
