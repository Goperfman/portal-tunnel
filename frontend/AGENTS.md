# Frontend AGENTS.md

High-signal constraints for the relay-server frontend. Only items expensive to rediscover.

## Frontend-Backend Contracts

1. **Public list data comes from `/state`.**
   Go shape is `types.PublicStateResponse`; TS shape is `src/types/api.ts`.
   - Why: the Go relay is API-only. Do not reintroduce Go HTML data injection for public lease state.

2. **API path constants require dual maintenance.**
   Go definitions live in `../types/paths.go`; TS duplicates live in `src/lib/apiPaths.ts`.
   - Why: no codegen. A path mismatch produces 404s.

3. **API envelope shape must match across Go and TS.**
   All JSON control-plane responses use `{ ok, data?, error?: { code, message } }`.
   Go shape is `types.APIEnvelope` in `../types/api.go`; Go writers live in `../utils/api.go`; TS parser lives in `src/lib/apiClient.ts`.
   - Why: backend responses that skip the envelope surface as `invalid_envelope` in the frontend.

4. **Admin auth uses bearer tokens returned by `/admin/auth/login`.**
   `src/hooks/useAuth.ts` stores the token through `src/lib/adminAuthToken.ts`; `src/lib/apiClient.ts` adds it to `/admin/*` requests as `Authorization: Bearer ...`.
   - Why: the relay admin API must be usable by any separately hosted frontend without credentialed cookie CORS state.

5. **`VITE_PORTAL_API_BASE_URL` is the only built-in API origin knob.**
   Leave it empty for same-origin development/proxying, or set it at build/dev time for a separately hosted relay API.
   - Why: runtime-generated config files couple the static frontend bundle back to deployment state.

6. **Admin state reads are aggregated through `/admin/state`.**
   `src/hooks/useAdmin.ts` expects `{ settings, leases }`; all setting writes go through `/admin/settings` with the full settings object.
   - Why: splitting those reads across multiple endpoints reintroduces extra request coordination and drift in the admin bootstrap path.

7. **Lease/AdminLease JSON casing is snake_case.**
   Go `Lease`/`AdminLease` JSON tags live in `../types/identity.go`; TS mirrors the wire shape in `src/types/api.ts`.
   - Why: the frontend should not depend on Go's implicit PascalCase encoder output.

8. **Admin policy writes identify targets in the JSON body.**
   Lease policy writes use `/admin/lease-policy` with `identity_key`; IP policy writes use `/admin/ip-policy` with `ip`.
   - Why: path encoding rules add a second contract surface and are easy to drift across Go and TS.

9. **Lease metadata has a wire type and a UI parser.**
   Go `LeaseMetadata` (`../types/identity.go`) mirrors TS `LeaseMetadata` (`src/types/api.ts`). UI display defaults are owned by `src/lib/metadata.ts`.
   - Why: API contract fields and UI fallback behavior should not be mixed.

10. **ApprovalMode is a closed two-value enum: `"auto"` | `"manual"`.**
   TS `normalizeApprovalMode()` (`src/hooks/useAdmin.ts`) collapses any non-`"manual"` value to `"auto"`.
   - Why: adding a third mode in Go without updating the TS normalizer silently collapses it to "auto".

## Frontend Conventions

1. **Do not use `useCallback` in new code.**
   React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`) handles memoization automatically.
   - Why: manual `useCallback` is redundant with the compiler and adds noise.

2. **Feature state lives in page-level hooks and is prop-drilled. No global state library.**
   `useServerList`, `useAdmin`, and `useAuth` own feature state at the page level. Theme is the exception; it uses `ThemeProvider`. `localStorage` persistence should silently fall back on errors.
   - Why: the prop-drilling pattern for feature state is intentional. Adding shared state providers for feature data changes the data flow architecture.

3. **Only `handleBPSChange` uses optimistic update with rollback.**
   All other admin actions use `runAdminAction()` which awaits the API call then refreshes via `fetchData()`.
   - Why: treating other admin handlers as optimistic will skip the server-refresh step and show stale data.
