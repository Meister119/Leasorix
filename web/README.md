# web

Leasorix web UI (Vite + React + TypeScript + Tailwind). The API client is
generated from `../internal/api/openapi.yaml` (`npm run gen`). `make build`
in the repository root builds the UI and embeds it into the binary with
`go:embed` (build tag `ui`).

```sh
npm ci
npm run dev        # Vite dev server, proxies /api -> LEASORIX_URL (default https://127.0.0.1:8067)
npm test           # Vitest
npm run typecheck
```

Texts are English by default and German can be selected in the UI. Every
module declares its texts with `messages({ en, de })` from `src/i18n`; the
compiler checks that both languages have the same keys.

The Playwright E2E tests run via `make test-e2e` (root, network namespaces).
