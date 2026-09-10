# @superdurable/superagent-ui

Transport-free React components for building SuperAgent conversation surfaces.
The package owns presentation and local interaction behavior only. Applications
remain responsible for API calls, durable state, routing, retries, and mapping
their domain objects into the exported view models.

## Install

```bash
npm install @superdurable/superagent-ui react
```

React 18.2 and React 19 are supported through the package's peer dependency.
Import the stylesheet once from the consuming application:

```tsx
import {
  ConversationComposer,
  MarkdownContent,
  PendingMessageQueue,
  PendingQuestionBatch,
} from "@superdurable/superagent-ui";
import "@superdurable/superagent-ui/styles.css";
```

`ConversationComposer` is controlled. `PendingMessageQueue` receives plain view
models and reports semantic actions by item ID, so it has no knowledge of Dex,
HTTP, generated clients, or product-specific stores. `MarkdownContent` renders
GitHub Flavored Markdown without enabling raw HTML.

`PendingQuestionBatch` handles one-to-three-question navigation, predefined and
free-form answers, local draft review, and one atomic ordered `onSubmit` call.
The application keeps ownership of the durable call ID and transport request.
Key the component by that stable batch identity when replacing one pending batch
with another so React resets its local drafts.

All owned selectors use the `sa-` prefix. Consumers can theme the components
with the documented `--sa-*` custom properties in `styles.css`. A few legacy
class names remain in the markup for compatibility with the SuperAgent browser
application's stable full-stack test selectors; consumers should target the
namespaced classes for styling.
