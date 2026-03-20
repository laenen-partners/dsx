# ADR-0004: Base Layout — The Required Foundation

## Status

Accepted

## Context

WebX applications are built from three cooperating layers: a Go middleware pipeline, a base HTML layout, and a set of backend SSE helpers (`ds.Send`). These layers are not independent — each one depends on specific DOM elements, meta tags, or context values provided by the others.

The question is: can an application use WebX piecemeal — picking the components it likes and skipping the rest? The answer is no: the base layout is a **required dependency**, not an optional convenience. This ADR explains why.

### The contract between layers

WebX's architecture has an implicit contract:

```
Middleware (dsx.Middleware)
    │
    │ populates Context: SessionID, CSRFToken, Theme
    │
    ▼
Base Layout (layouts.Base)
    │
    │ reads Context, renders:
    │   <meta name="csrf-token">        ← CSRF protection
    │   <div id="drawer-panel">         ← drawer target
    │   <div id="modal-panel">          ← modal target
    │   <div id="toast-container">      ← toast target
    │   @stream.Connect()               ← SSE connection
    │   data-theme="..."                ← theme switching
    │
    ▼
Backend Helpers (ds.Send.*)
    │
    │ target those DOM elements by fixed ID:
    │   ds.Send.Toast  → #toast-container (append mode)
    │   ds.Send.Modal  → #modal-panel (replace content)
    │   ds.Send.Drawer → #drawer-panel (replace content)
    │   ds.Post/Put/…  → reads <meta name="csrf-token">
    │   stream.Connect → opens SSE, pushes stale signals
    │
    ▼
Browser
```

Every arrow is a hard dependency. Remove any element from the layout and a downstream feature breaks — usually silently.

## Decision

### The base layout is required

`layouts.Base` is the minimal HTML document that satisfies the contract. It is 40 lines of templ and sets up exactly six things:

```
layouts.Base(props)
├── <html data-theme={props.Theme}>          ← 1. Theme
├── <head>
│   ├── <meta name="csrf-token">             ← 2. CSRF token
│   └── {props.Head}                         ← 3. CSS + Datastar script (app-provided)
├── <body>
│   ├── {children...}                        ← page content
│   ├── <div id="drawer-panel">              ← 4. Drawer target
│   ├── <div id="modal-panel">               ← 5. Modal target
│   ├── @stream.Connect()                    ← 6. SSE stream connection
│   └── <div id="toast-container">           ← 7. Toast target
└── </body>
```

Each element exists because something else depends on it:

### 1. CSRF meta tag

```html
<meta name="csrf-token" content={ props.CSRFToken }/>
```

The `ds` package generates JavaScript expressions for mutating HTTP verbs (POST, PUT, PATCH, DELETE) that include a CSRF header:

```go
const csrfJS = `document.querySelector('meta[name=csrf-token]')?.content||''`
// Every ds.Post, ds.Put, ds.Patch, ds.Delete injects:
// headers: {'X-CSRF-Token': <csrfJS>}
```

The middleware (`dsx.Middleware`) validates this header on every non-GET request using constant-time HMAC comparison. Without the meta tag, the JavaScript expression returns an empty string and **every mutation fails with 403 Forbidden**.

The token value comes from `dsx.Context.CSRFToken`, populated by the middleware from a signed `HttpOnly` cookie (`dsx_csrf`). The layout bridges the middleware's server-side token to the client-side JavaScript that needs to send it back.

**Without it**: Every `ds.PostOnce`, `ds.Put`, `ds.Delete`, and form submission returns 403.

### 2. Drawer container

```html
<div id="drawer-panel"></div>
```

`ds.Send.Drawer()` renders a templ component into a slide-in panel by replacing the contents of `#drawer-panel`:

```go
const DrawerContainerID = "drawer-panel"

func (s *Sender) Drawer(sse *datastar.ServerSentEventGenerator, content templ.Component, ...) error {
    // renders overlay + panel into #drawer-panel
    return sse.PatchElements(html)
}
```

The drawer HTML includes an overlay backdrop and a right-side panel. `ds.Send.HideDrawer()` empties the container to close it.

**Without it**: `PatchElements` targets `#drawer-panel`, finds nothing, and the drawer never appears. No error is logged — the SSE event is sent but the DOM has no matching element.

### 3. Modal container

```html
<div id="modal-panel"></div>
```

Identical pattern to the drawer. `ds.Send.Modal()` and `ds.Send.Confirm()` replace `#modal-panel` content with a centered dialog:

```go
const ModalContainerID = "modal-panel"
```

**Without it**: Modals and confirmation dialogs never render.

### 4. Toast container

```html
<div id="toast-container" class="toast toast-end toast-bottom z-50"></div>
```

`ds.Send.Toast()` appends notifications into this container using Datastar's append mode:

```go
const ToastContainerID = "toast-container"

func patchToast(sse *datastar.ServerSentEventGenerator, html string) error {
    return sse.PatchElements(html,
        datastar.WithSelector("#"+ToastContainerID),
        datastar.WithModeAppend(),
    )
}
```

The DaisyUI `toast toast-end toast-bottom` classes position the container as a fixed stack at the bottom-right. Each toast is an `alert` with auto-dismiss via `setTimeout`.

**Without it**: Toast notifications are lost. Mutation handlers that show success/error feedback appear to do nothing.

### 5. Stream connection

```go
@stream.Connect()
```

`stream.Connect()` is a templ component that reads `dsx.Context.Watchers` — the scopes accumulated during page render by `stream.Attrs()` and `stream.WatchEffect()` calls — and opens a persistent SSE connection.

It renders a hidden `<div>` with:
- `data-signals` initializing stale flags for all watched scopes
- `data-init` opening `@get(streamURL)` with `requestCancellation: 'disabled'`

**Placement matters**: `stream.Connect()` is placed **after** `{ children... }` so that all components have finished registering their watchers before the connection is built. If it were in the `<head>` or before the content, it would see zero watchers and open an empty stream.

**Without it**: Reactive components (`stream.Attrs`, `stream.WatchEffect`) register watchers that are never consumed. No SSE connection opens. `bus.Notify*()` publishes messages that no one receives. The UI stays static.

### 6. Theme attribute

```html
<html data-theme={ props.Theme }>
```

DaisyUI uses CSS custom properties scoped by the `data-theme` attribute. All component classes (`btn-primary`, `bg-base-100`, `text-base-content`) resolve to theme-specific colors through these variables.

The theme value comes from `dsx.Context.Theme`, read by the middleware from the `dsx_theme` cookie. The `themecontroller.IconToggle` component writes to this cookie and updates the `data-theme` attribute live.

**Without it**: DaisyUI falls back to its default theme. Theme switching via the controller has no effect because there's no `data-theme` attribute to update.

### 7. CSS and Datastar script (app-provided via `Head`)

The base layout provides a `Head` slot for the application to inject its stylesheet and Datastar script:

```go
@layouts.Base(layouts.BaseProps{
    Head: showcaseHead(), // <link> + <script>
})
```

These are not hardcoded in the base layout because the asset paths are app-specific (the base layout is a library component, not an application template). But they are **required**:

- **Without Tailwind/DaisyUI CSS**: Every DaisyUI class is ignored. The page renders as unstyled HTML.
- **Without Datastar script**: Every `data-on:click`, `data-show`, `data-effect`, `data-signals`, `data-bind`, and `data-init` attribute is inert. No interactivity, no SSE connections, no signal management.

### Why not make them optional?

The elements above could theoretically be conditionally rendered — only include `#toast-container` if the app uses toasts, only include `stream.Connect()` if scopes are registered, etc.

This was considered and rejected for three reasons:

**1. Silent failures are worse than unused elements.** An empty `<div id="drawer-panel">` costs zero bytes of visible UI and zero runtime overhead. But forgetting to include it when a handler calls `ds.Send.Drawer()` produces a completely silent failure — the SSE event is sent, the client receives it, Datastar processes it, but the DOM selector matches nothing. There is no error in the console, no server log, no indication that anything went wrong. The developer sees a button click that does nothing and has to trace through the SSE, Datastar, and DOM layers to find the missing container.

**2. The elements are interdependent.** CSRF is needed by any mutation. Mutations commonly show toasts on success. Toasts need the container. Drawers contain forms. Forms submit mutations. Mutations invalidate scopes. Scopes need the stream. In practice, any non-trivial page needs all of them.

**3. The cost of including everything is negligible.** The six infrastructure elements add ~300 bytes to the HTML. Three are empty `<div>` tags. One is a `<meta>` tag. One is a conditional hidden `<div>` (only rendered when scopes exist). The theme attribute is a single HTML attribute. There is no JavaScript overhead, no additional HTTP requests, no runtime cost for unused containers.

### The Dashboard layout builds on Base

`layouts.Dashboard` wraps `layouts.Base` and adds application chrome — sidebar navigation, navbar, theme toggle, user menu, and an optional detail panel. It inherits all of Base's infrastructure automatically:

```go
templ Dashboard(props DashboardProps) {
    @Base(props.BaseProps) {
        // sidebar, navbar, main content area
        { children... }
    }
}
```

Applications that don't want the dashboard chrome use `layouts.Base` directly. Both paths get the same infrastructure.

### Integration with middleware

The middleware and layout form a two-phase pipeline:

**Phase 1 — Middleware** (`dsx.Middleware`):
1. Reads or creates session ID → `dsx_session` cookie
2. Reads or creates signed CSRF token → `dsx_csrf` cookie
3. Validates CSRF header on mutating requests → 403 on mismatch
4. Reads theme preference → `dsx_theme` cookie
5. Populates `dsx.Context{SessionID, CSRFToken, Theme}`

**Phase 2 — Layout** (`layouts.Base`):
1. Reads `CSRFToken` from context → `<meta name="csrf-token">`
2. Reads `Theme` from context → `data-theme` attribute
3. Renders infrastructure containers → `#drawer-panel`, `#modal-panel`, `#toast-container`
4. Renders `stream.Connect()` → reads `Watchers` and `StreamURL` from context

The layout cannot function without the middleware (no CSRF token, no theme). The `ds.Send` helpers cannot function without the layout (no target containers). The middleware serves no purpose without the layout rendering its values into the DOM. They are a unit.

### Security headers

`dsx.SecurityHeadersMiddleware()` sets response headers that complement the layout:

| Header | Value | Relevance |
|--------|-------|-----------|
| `Content-Security-Policy` | `script-src 'self' 'unsafe-eval'` | `unsafe-eval` required because Datastar evaluates JS expressions client-side |
| `X-Frame-Options` | `DENY` | Prevents clickjacking on the layout's interactive elements |
| `X-Content-Type-Options` | `nosniff` | Prevents MIME-sniffing of SSE responses |
| `Strict-Transport-Security` | (HTTPS only) | Ensures cookies (`HttpOnly`, `SameSite: Lax`) are sent over HTTPS |

These headers protect the DOM structure the layout creates. Without the CSP, inline scripts could access the CSRF meta tag. Without frame protection, the layout's modals/drawers could be targeted by clickjacking.

## Consequences

### Positive

- **No silent failures**: Every WebX feature has its DOM target guaranteed by the base layout
- **Zero configuration**: Applications get CSRF, toasts, modals, drawers, streaming, and theming by wrapping content in `layouts.Base`
- **Consistent structure**: Every WebX application has the same HTML skeleton, making debugging and tooling predictable
- **Minimal overhead**: The infrastructure adds ~300 bytes of HTML and zero runtime cost for unused features

### Negative

- **Mandatory dependency**: You cannot use `ds.Send.Toast()` without `layouts.Base` (or manually recreating its elements). This is intentional but means WebX is not a pick-and-mix component library
- **Fixed element IDs**: `drawer-panel`, `modal-panel`, `toast-container` are hardcoded constants shared between the layout and the `ds` package. Renaming them requires changing both
- **CSP includes `unsafe-eval`**: Datastar's client-side expression evaluation requires `unsafe-eval` in the script-src directive, which is a weaker CSP than some security policies allow

### Trade-offs

- **Convention over configuration**: The base layout makes choices (container positions, toast stacking direction, z-index layering) that applications inherit. This reduces flexibility but eliminates an entire class of integration bugs.
- **Library vs. framework**: By requiring the base layout, WebX is closer to a framework than a component library. Applications that only want the UI components (buttons, cards, inputs) without the interactive infrastructure can use the `ui/` packages directly — they have no dependency on the layout. But the moment you use `ds.Send`, streaming, or CSRF-protected mutations, you need the base layout.
