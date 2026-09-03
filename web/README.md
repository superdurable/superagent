# Superagent web application

This React application consumes only the TypeScript client generated from
`api/openapi.yaml`. Do not hand-write HTTP request or response models.

The application restores a Flow with one generated Snapshot request and applies
generated event polls for live updates. It does not call the legacy history,
describe, status, or message-queue read endpoints. The complete original
frontend remains unchanged under `reference/python/ai-agent/` as the parity
oracle.

## Commands

```bash
npm ci
npm run generate:api
npm run typecheck
npm run lint
npm run build
```

The production build is written to the ignored `web/dist/` directory. It is a
standalone deployment artifact. The Go binary contains no frontend files.
Generated API files are committed. `make check-generated` verifies that both
the ogen server and Hey API client have zero drift.

The build copies `public/config.json` into the artifact. The browser loads it
before rendering. The file configures the generated Fetch client from
`apiOrigin`. Deployments can replace that JSON file without rebuilding the
bundle.

For local development, start Superagent with the frontend origin allowlisted:

```bash
SUPERAGENT_HTTP_ALLOWED_ORIGINS=http://127.0.0.1:3000 ./bin/superagent
python3 -m http.server 3000 --directory web/dist
```

Open `http://127.0.0.1:3000/`. Production `apiOrigin` values must use HTTPS.
Serve `config.json` with `Cache-Control: no-store`. Configure the static host's
Content Security Policy to allow connections only to the selected API origin.

## Durable and live state

One reducer action atomically replaces application history, Agent description,
queued messages, steered messages, and Run identity from `/snapshot`. Three
cancellable `/events` polls add assistant text, reasoning summaries, and
structured activity. Disconnects and command completion reconcile with another
Snapshot. Queue mutations optimistically update by stable message ID and then
reconcile; no four-read compatibility layer exists.
