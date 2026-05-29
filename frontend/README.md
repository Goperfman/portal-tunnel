# Relay Server Frontend

React + TypeScript frontend for relay server discovery and onboarding.

## Tech Stack

- React 19
- TypeScript
- Vite 7
- Tailwind CSS 4
- shadcn/ui (Radix-based)
- Lucide React
- @ssgoi/react
- React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`)

## Core Behavior

The Go relay is API-only. This frontend is a standalone Vite app that talks to
the relay over the JSON API and does not receive server-side injected lease
data.

- Public relay state is loaded from `/api/public/snapshot`.
- Admin state is loaded from `/admin/snapshot`.
- All JSON API responses use the `{ ok, data?, error? }` envelope parsed by `src/lib/apiClient.ts`.
- `VITE_PORTAL_API_BASE_URL` points the frontend at a relay API origin. Admin auth uses a bearer token returned by `/admin/auth/login`.

## Project Structure

```text
frontend/
  src/
    components/
    hooks/
      useServerList.ts
      useAdmin.ts
      useList.ts
      useAuth.ts
    lib/
      apiClient.ts
      apiPaths.ts
      metadata.ts
    pages/
      Admin.tsx
      ServerDetail.tsx
      ServerList.tsx
    types/
      lease.ts
    App.tsx
    main.tsx
    index.css
  index.html
  package.json
  tsconfig.json
  vite.config.ts
```

## Install and Build

```bash
cd frontend
npm install
npm run build
```

Build output goes to `frontend/dist/`.

## Development

```bash
cd frontend
npm run dev
```

Default dev URL: `http://localhost:5173`.

To run against a relay server on another origin, build or run the frontend with the public relay API URL:

```bash
VITE_PORTAL_API_BASE_URL=https://relay.example.com npm run dev
```

## Docker

The frontend Docker image serves the built Vite app with nginx and proxies API
paths to `portal-api:4017` in Docker Compose. The app uses same-origin relative
API paths, so it does not need runtime config file generation.

```bash
docker compose up -d portal-frontend
```

## NPM Scripts

| Script | Purpose |
| --- | --- |
| `npm run dev` | Start the Vite development server. |
| `npm run build` | Type-check and build production assets. |
| `npm run lint` | Run ESLint. |
| `npm run typecheck` | Run TypeScript checking. |
| `npm test` | Run Vitest. |
| `npm run preview` | Preview the production bundle. |

## Relay Integration

Relay server exposes:

- `/` - relay API identity response
- `/api/public/snapshot` - public leases and landing-page state
- `/tunnel/status` - tunnel readiness check used by the command form
- `/thumbnail/{hostname}` - cached generated screenshots
- `/install.sh` and `/install.ps1` - CLI installers
- `/admin/*` - admin API/control endpoints
- `/sdk/*` - SDK/control endpoints
- `/discovery` - relay discovery when enabled

## Notes

- API path constants are duplicated in Go (`types/paths.go`) and TS (`src/lib/apiPaths.ts`).
- Lease JSON field casing is intentionally mixed to match Go's current wire output; see `src/types/lease.ts`.
- Radix Select values cannot be empty strings. Use stable values such as `"all"` and `"default"`.
