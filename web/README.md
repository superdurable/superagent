# SuperAgent web application

This React application consumes only the TypeScript client generated from
`api/openapi.yaml`. Do not hand-write HTTP request or response models.

Reusable, transport-free conversation components live in the local npm
workspace `packages/superagent-ui`. The Web application consumes that package
through its public exports and explicitly maps generated state into its view
models. Network calls, durable-state reconciliation, and product orchestration
remain in this application.

The application restores a Flow with one generated Snapshot request, lists the
configured recent tail of each Stream, and resumes generated event polls from
the newest returned token. Hidden pages release live requests. Command
submission waits for their cancellation to settle before sending the mutation.

## Commands

```bash
npm ci
npm run generate:api
npm run typecheck
npm run lint
npm run build
```

The root scripts build, type-check, and test the shared UI workspace as part of
the corresponding Web command. The UI package can also be checked directly with
`npm run build --workspace @superdurable/superagent-ui` and packed with
`npm pack --workspace @superdurable/superagent-ui`.

The production build is written to the ignored `web/dist/` directory. It is a
standalone deployment artifact. The Go binary contains no frontend files.
Generated API files are committed. `make check-generated` verifies that both
the ogen server and Hey API client have zero drift. JavaScript and CSS filenames
include content hashes so a deployment cannot reuse stale browser assets.

The build copies `public/config.json` into the artifact. The browser loads it
before rendering. The file configures the generated Fetch client from
`apiOrigin`. Deployments can replace that JSON file without rebuilding the
bundle.

For local development, start SuperAgent with the frontend origin allowlisted:

```bash
SUPERAGENT_HTTP_ALLOWED_ORIGINS=http://127.0.0.1:3000 ./bin/superagent
node script/serve-web.mjs --directory web/dist --port 3000
```

Open `http://127.0.0.1:3000/`. Production `apiOrigin` values must use HTTPS.
Serve `index.html` with `Cache-Control: no-cache`, hashed assets as immutable,
and `config.json` with `Cache-Control: no-store`. Configure the static host's
Content Security Policy to allow connections only to the selected API origin.

## Durable and live state

One reducer action atomically replaces application history, Agent description,
queued messages, steered messages, and Run identity from `/snapshot`. Three
cancellable `/events/recent` reads recover a bounded Stream tail before
`/events` polls add assistant text, reasoning summaries, and structured
activity. Disconnects and command completion reconcile with another Snapshot.
Every command cancels and joins live polls before issuing its mutation request
so browser connection limits cannot delay durable acceptance.
Queue mutations optimistically update by stable message ID and then reconcile.
The timeline follows the latest content until the user scrolls upward. The
queue starts expanded as a height-bounded list of truncated one-line messages
and inline actions.
