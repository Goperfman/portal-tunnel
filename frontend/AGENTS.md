# Frontend AGENTS.md

High-signal constraints for the relay-server frontend. Only items expensive to rediscover.

## Frontend-Backend Contracts

1. **Public list data comes from `/api/public/snapshot`.**
   Go shape is `types.PublicSnapshotResponse`; TS shape is `src/types/lease.ts`.
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

6. **Admin state reads are aggregated through `/admin/snapshot`.**
   `src/hooks/useAdmin.ts` expects one payload carrying `leases`, settings, and `approval_mode`.
   - Why: splitting those reads across multiple endpoints reintroduces extra request coordination and drift in the admin bootstrap path.

7. **Lease/AdminLease JSON casing is a mixed implicit/explicit contract.**
   `Lease` (`../types/identity.go`): `Name` has `json:"name"`, while `FirstSeenAt`, `LastSeenAt`, `Hostname`, `Ready`, and `Metadata` use Go's default PascalCase names. `AdminLease`: `IdentityKey` and `Address` have snake_case json tags, while `BPS`, `ClientIP`, `ReportedIP`, `IsApproved`, `IsBanned`, `IsDenied`, and `IsIPBanned` use PascalCase. TS types in `src/types/lease.ts` include the fields currently rendered or used by actions.
   - Why: adding a `json:"..."` tag to any currently untagged field silently changes the wire name and breaks the TS consumer.

8. **Admin lease paths use base64-url encoding with URI-component escaping.**
   TS `encodePathPart()` (`src/lib/apiPaths.ts`) does `btoa(value)` then replaces `+/=` with `-/_/""` before `encodeURIComponent()`. Go decodes via `utils.DecodeBase64URLString()`.
   - Why: two-layer codec. Changing either side silently produces 400s on admin lease actions.

9. **`Metadata` is typed `unknown` in TS but has a concrete Go struct.**
   Go `LeaseMetadata` (`../types/identity.go`) has more fields than the frontend renders. TS parses only rendered fields at runtime in `src/lib/metadata.ts`.
   - Why: adding or renaming a rendered Go metadata field silently drops data in the frontend. No compile-time contract exists.

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
