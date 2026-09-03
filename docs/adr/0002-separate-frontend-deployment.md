# ADR 0002: Deploy the frontend separately

## Status

Accepted on 2026-09-03.

## Context

The initial Phase 1 build embedded the React assets in the Go binary. Any
frontend fix therefore required rebuilding and replacing the API and Worker
process. The shared release unit also coupled browser caching to backend
lifecycles.

## Decision

The Go process serves only the OpenAPI surface. It contains no frontend assets
or fallback routes.

Webpack produces `web/dist` as an independent static artifact. The browser
loads `config.json` before rendering. That file supplies one validated API
origin and can be replaced without rebuilding the JavaScript bundle.

The backend permits cross-origin browser requests only from an exact configured
allowlist. Wildcards and credentialed CORS requests are unsupported. Same-origin
edge routing remains available without CORS configuration.

## Consequences

Frontend changes can deploy and roll back without replacing the backend. The
static host owns frontend caching, Content Security Policy, and SPA routing.

Operators must coordinate the frontend `apiOrigin` with the backend allowlist.
Production uses HTTPS. Loopback HTTP remains available for local development.
