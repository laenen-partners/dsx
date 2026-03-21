# ADR-002: DOM-Driven Real-Time Subscriptions via `<watch>` Elements
## Status: Accepted
## Supersedes: ADR-0001 (Signal-Derived SSE Subscriptions)
## Date: 2026-03-21
## Authors: Engineering Leadership

---

## Context

ADR-001 established a stateless, signal-derived SSE architecture. Three problems
remained that this ADR resolves together:

**Problem 1 — Subscription cleanup depended on developer discipline.**
Components had to explicitly call `__addTopic` on mount and `__removeTopic` on
close. A missing close handler caused a leaked subscription until the next reconnect.

**Problem 2 — Bus subscriber count scaled with connections × topics.**
With 1000 users each watching `"doc"` plus one open drawer the bus held 2000
subscriber channels. Every `Publish` iterated all of them inside a read lock.

**Problem 3 — Naive whole-component re-renders caused poor UX.**
Every signal caused the entire list or drawer to be replaced, regardless of
whether the user was interacting with it, whether the change was structural
(new row) or cosmetic (updated title), and whether events were arriving faster
than renders could complete. This produced flicker, lost scroll position, lost
hover state, lost inline interaction state, and out-of-order renders under load.

---

## Event Signature

All domain events follow a fixed three-part dot-separated signature:

```
  domain.id.action

  Examples:
    doc.123.created    ← new document created
    doc.123.updated    ← existing document updated
    doc.123.deleted    ← document deleted
    invoice.456.created
    invoice.456.updated
```

This structure is the foundation of every routing and filtering decision in the
system. Components use `action` to decide how to react. The architecture uses
`domain` to route to the correct fanout. The client uses `id` to match against
the specific entity it is displaying.

---

## Decision

We adopt a **DOM-driven, domain-fanout, action-aware SSE architecture** where:

- Components declare subscriptions via `<div data-watch="domain.id"/>` elements in HTML
- A `MutationObserver` worker tracks `<... data-wath=... >` presence and drives SSE reconnects
- The SSE stream pushes **Data-Star signal merges** carrying `{domain, id, action}`
- Components react via `data-on-signal-change__window` with action-aware expressions
- **List containers** re-fetch only on `created` or `deleted` — not `updated`
- **List rows** re-fetch individually on `updated` for their own ID
- **Detail drawers** show a stale banner on `updated` — user controls the reload
- One `DomainFanout` goroutine per entity type subscribes to the bus once, forever
- The fanout dispatches to matching connections outside the bus lock
- Auth is enforced on every SSE connect, reconnect, and fragment fetch

---

## How Data-Star Components Know to Reload

This is the central mechanism. Every component behaviour in this ADR derives
from understanding these four steps precisely.

### Step 1 — Server pushes a signal merge over SSE

When `bus.Publish("doc.123.updated")` fires, the SSE handler writes a
Data-Star signal merge to the client's open SSE stream:

```
data: {
  "dsEvent": {
    "domain": "doc",
    "id":     "123",
    "action": "updated"
  }
}
```

Data-Star receives this and **merges it into its client-side signal store**.
`$dsEvent` now equals `{domain:"doc", id:"123", action:"updated"}`.

### Step 2 — `data-on-signal-change__window` fires on every component

Data-Star evaluates every `data-on-signal-change__window` expression on the
page whenever any signal in the store changes. The `__window` modifier means
the listener is global — it fires for signal changes anywhere on the page,
not just within the element's own subtree.

```html
data-on-signal-change__window="
  $dsEvent?.domain === 'doc'
  && $dsEvent?.action === 'updated'
  && $dsEvent?.id === '123'
  && @get('/fragments/documents/123')
"
```

When `$dsEvent` becomes `{domain:"doc", id:"123", action:"updated"}`:
- All three conditions evaluate to `true`
- `@get('/fragments/documents/123')` fires
- Data-Star fetches the fragment and merges the response HTML

### Step 3 — Fragment endpoint returns fresh HTML

`GET /fragments/documents/123` re-renders from the database and returns HTML.
Data-Star merges it into the element with the matching `id` attribute.

### Step 4 — Action determines which component reacts

The same event reaches every component on the page. The `action` field lets
each component decide independently what to do:

```
  dsEvent = {domain:"doc", id:"123", action:"updated"}
          │
          ├──► List container:
          │    action === 'updated' → false → does nothing ✓
          │    (only reacts to created/deleted)
          │
          ├──► Row for doc:123:
          │    action === 'updated' → true
          │    id === '123'         → true
          │    @get('/fragments/documents/123/row') fires ✓
          │
          └──► Drawer for doc:123:
               action === 'updated' → true
               id === '123'         → true
               shows stale banner — user decides when to reload ✓

  dsEvent = {domain:"doc", id:"789", action:"created"}
          │
          ├──► List container:
          │    action === 'created' → true
          │    @get('/fragments/documents') fires ✓ (new row must appear)
          │
          ├──► Row for doc:123:
          │    id === '789' → false → does nothing ✓
          │
          └──► Drawer for doc:123:
               id === '789' → false → does nothing ✓
```

### Why `__window`

Without `__window`, `data-on-signal-change` only fires when the signal changes
within the element's own DOM subtree. Because `$dsEvent` is a page-level signal
written by the SSE stream, every component needs `__window` to receive it.

### Why `?.` (Optional Chaining)

`$dsEvent` is initialised as `{}` in `data-signals`. On page load,
`$dsEvent?.domain` evaluates to `undefined`, not an error, preventing
spurious fetches before any real event arrives.

---

## UX Patterns Per Component Type

The re-fetch granularity is a **product decision per component**, not a
one-size-fits-all architectural default. Getting this wrong causes the UX
problems described in the context section.

### The UX Problems We Are Solving

```
  Whole-list re-render on every update:
  ──────────────────────────────────────────────────────────────────────
  User reading row 8 of a 20-item list
  Another user saves doc:15 (off screen, near bottom)
  Entire list HTML replaced
  User's eye position disrupted
  Hover state on row 8 gone
  Scroll position may jump ✗

  Re-fetch storm under rapid events:
  ──────────────────────────────────────────────────────────────────────
  Autosave fires every 2s on doc:123
  Each save → signal → @get fires
  Three concurrent requests in flight
  Responses arrive out of order
  List renders state from t=4s, then overwrites with t=2s
  User sees the list go backwards ✗

  Drawer wipes user interaction state:
  ──────────────────────────────────────────────────────────────────────
  User reading long drawer, scrolled 80% down
  Collaborator saves the document
  Drawer re-renders from top
  User loses scroll position
  If user was mid-select, selection gone ✗

  Missing event during reconnect gap:
  ──────────────────────────────────────────────────────────────────────
  Drawer opens → <watch> added → SSE reconnects (300ms + round-trip)
  During this gap: doc:123 is edited by another user
  SSE reconnects — edit event is in the past, never replayed
  Drawer shows stale data indefinitely ✗
```

### Pattern 1 — List Container (structure changes only)

```
  Reacts to:   created, deleted
  Ignores:     updated  (rows handle their own updates)
  Behaviour:   full list re-fetch only when rows appear or disappear
```

```go
templ DocumentListComponent() {
    <div
        id="document-list"
        data-on-load="@get('/fragments/documents')"
        data-on-signal-change__window={`
            $dsEvent?.domain === 'doc'
            && ($dsEvent?.action === 'created' || $dsEvent?.action === 'deleted')
            && @get('/fragments/documents')
        `}
    >
        <p>Loading...</p>
    </div>
}
```

```
  doc.456.updated → list does nothing ✓  (row handles it)
  doc.789.created → list re-fetches ✓   (new row must appear)
  doc.123.deleted → list re-fetches ✓   (row must disappear)
```

### Pattern 2 — List Row (in-place update, debounced)

```
  Reacts to:   updated for its own ID only
  Ignores:     created, deleted, other IDs
  Behaviour:   replaces only this row's HTML, debounced to absorb rapid saves
```

```go
templ DocumentRow(doc model.Document) {
    <div
        id={fmt.Sprintf("doc-row-%d", doc.ID)}
        data-on-signal-change__window.debounce_500ms={fmt.Sprintf(`
            $dsEvent?.domain === 'doc'
            && $dsEvent?.action === 'updated'
            && $dsEvent?.id === '%d'
            && @get('/fragments/documents/%d/row')
        `, doc.ID, doc.ID)}
    >
        <span class="title">{ doc.Title }</span>
        <span class="time">{ doc.UpdatedAt.Format("15:04:05") }</span>
    </div>
}
```

```
  doc.123.updated → row 123 re-renders in place ✓
  doc.456.updated → row 123 does nothing ✓
  doc.123.updated (5x in 1s, autosave) → debounce absorbs 4, 1 re-fetch ✓
  Rest of list untouched, scroll position preserved ✓
```

### Pattern 3 — Detail Drawer, Read-Only Content

```
  Reacts to:   updated for its own ID
  Behaviour:   auto-reloads immediately (user is reading, not editing)
  Guard:       refetch on reconnect to close the event gap
```

```go
templ DocumentDrawerReadOnly(doc model.Document) {
    <div id="document-drawer">
        <watch id={fmt.Sprintf("doc.%d", doc.ID)}></watch>

        <div
            data-on-signal-change__window={fmt.Sprintf(`
                ($dsEvent?.domain === 'doc'
                && $dsEvent?.action === 'updated'
                && $dsEvent?.id === '%d'
                && @get('/fragments/documents/%d'))
                || ($sseConnID && @get('/fragments/documents/%d'))
            `, doc.ID, doc.ID, doc.ID)}
        >
            <h2>{ doc.Title }</h2>
            <p>{ doc.Body }</p>
        </div>
    </div>
}
```

The second condition (`$sseConnID && @get(...)`) fires whenever `$sseConnID`
changes — which happens on every SSE reconnect. This closes the event gap:
any edit that occurred during the reconnect window is recovered immediately
after the new connection is established.

### Pattern 4 — Detail Drawer, User-Edited Content

```
  Reacts to:   updated for its own ID
  Behaviour:   shows stale banner — user controls reload
  Rationale:   auto-reload would destroy user's in-progress edits
```

```go
templ DocumentDrawerEditable(doc model.Document) {
    <div id="document-drawer">
        <watch id={fmt.Sprintf("doc.%d", doc.ID)}></watch>

        // Banner is hidden by default.
        // data-on-signal-change makes it visible when a remote update arrives.
        // Reload button re-fetches and replaces the whole drawer.
        <div
            id="drawer-stale-banner"
            style="display:none"
            data-on-signal-change__window={fmt.Sprintf(`
                $dsEvent?.domain === 'doc'
                && $dsEvent?.action === 'updated'
                && $dsEvent?.id === '%d'
                && (__showStaleBanner())
            `, doc.ID)}
        >
            <span>This document was updated by another user.</span>
            <button
                data-on-click={fmt.Sprintf(
                    `@get('/fragments/documents/%d')`, doc.ID,
                )}
            >Load latest</button>
            <button onclick="document.getElementById('drawer-stale-banner').style.display='none'">
                Dismiss
            </button>
        </div>

        <h2>{ doc.Title }</h2>
        <textarea>{ doc.Body }</textarea>
    </div>
}
```

```javascript
// Small helper — show the banner without replacing any content
window.__showStaleBanner = function() {
    const el = document.getElementById('drawer-stale-banner')
    if (el) el.style.display = 'block'
}
```

### Pattern 5 — Dashboard Stat / Counter

```
  Reacts to:   created, updated, deleted for its domain
  Behaviour:   immediate re-fetch, no debounce (accuracy matters more than stability)
```

```go
templ DocumentCountStat() {
    <div
        id="document-count"
        data-on-load="@get('/fragments/documents/count')"
        data-on-signal-change__window={`
            $dsEvent?.domain === 'doc'
            && @get('/fragments/documents/count')
        `}
    >
    </div>
}
```

### Pattern 6 — Notification / Activity Feed

```
  Reacts to:   created only (existing items don't change in a feed)
  Behaviour:   prepend new item fragment — never replaces existing items
```

```go
templ ActivityFeed() {
    <div
        id="activity-feed"
        data-on-load="@get('/fragments/activity')"
        data-on-signal-change__window={`
            $dsEvent?.action === 'created'
            && @get('/fragments/activity/' + $dsEvent.domain + '/' + $dsEvent.id + '/event')
        `}
    >
    </div>
}
```

The server returns a single new event row fragment with a Data-Star prepend
directive rather than the full feed, so existing items are never touched.

---

## UX Decision Matrix

```
  Component              Reacts to actions     Re-fetch scope     Debounce
  ──────────────────────────────────────────────────────────────────────────
  List container         created, deleted      full list          no
  List row               updated (own id)      single row         yes (500ms)
  Drawer (read-only)     updated (own id)      full drawer        no
  Drawer (editable)      updated (own id)      stale banner only  no
  Dashboard stat         any                   stat fragment      no
  Activity feed          created               single new item    no

  Rule of thumb:
    Is user likely to be interacting with this component?
      yes + they have edits → stale banner
      yes + read-only       → auto-reload
      no                    → auto-reload
    Does the whole thing change or just a row?
      just a row            → row-level fragment
      whole thing           → full component
    How frequent are events?
      high (autosave)       → debounce
      low (manual saves)    → immediate
```

---

## Architecture

### System Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│                                BROWSER                                   │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │                  Data-Star Signal Store                            │ │
│  │                                                                    │ │
│  │  $dsEvent   : {domain:"doc", id:"123", action:"updated"}          │ │
│  │  $sseConnID : "uuid-abc"                                          │ │
│  │  $ssePing   : 1742558400000                                       │ │
│  └────────────────────────────────────────────────────────────────────┘ │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │                            DOM                                     │ │
│  │  <watch id="doc"/>         ← list subscription (always present)   │ │
│  │  <watch id="doc.123"/>     ← drawer subscription (when open)      │ │
│  └──────────────────────────┬─────────────────────────────────────────┘ │
│                             │ MutationObserver                           │
│                             ▼                                            │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │                   Watch Worker (~80 lines JS)                      │ │
│  │                                                                    │ │
│  │  Observes DOM for <watch> additions/removals                       │ │
│  │  On change: collectTopics() → diff → debounce(300ms) → reconnect  │ │
│  │  On SSE open: re-collect from live DOM (closes reconnect gap)      │ │
│  └──────────────────────────┬─────────────────────────────────────────┘ │
│                             │ GET /sse/events?watch=doc,doc.123          │
│                             ▼                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐ │
│  │  SSE Connection                                                    │ │
│  │  Data-Star processes signal merges automatically                   │ │
│  │  $dsEvent updated → data-on-signal-change__window fires everywhere │ │
│  └─────────────────────────────────────────────────────────────────────┘ │
│                                                                          │
│  ┌───────────────────────────────────┐                                  │
│  │  #document-list                   │                                  │
│  │                                   │                                  │
│  │  data-on-signal-change__window:   │                                  │
│  │    domain==='doc'                 │                                  │
│  │    && action==='created'          │                                  │
│  │       || action==='deleted'       │                                  │
│  │    → @get /fragments/documents    │                                  │
│  │                                   │                                  │
│  │  ┌─────────────────────────────┐  │                                  │
│  │  │ doc-row-123                 │  │                                  │
│  │  │ data-on-signal-change__window  │                                  │
│  │  │   domain==='doc'            │  │                                  │
│  │  │   && action==='updated'     │  │                                  │
│  │  │   && id==='123'             │  │                                  │
│  │  │   → @get .../123/row       │  │                                  │
│  │  └─────────────────────────────┘  │                                  │
│  │  ┌─────────────────────────────┐  │                                  │
│  │  │ doc-row-456  (same pattern) │  │                                  │
│  │  └─────────────────────────────┘  │                                  │
│  └───────────────────────────────────┘                                  │
│                                                                          │
│  ┌───────────────────────────────────┐                                  │
│  │  #document-drawer                 │                                  │
│  │  <watch id="doc.123"/>            │                                  │
│  │                                   │                                  │
│  │  data-on-signal-change__window:   │                                  │
│  │    domain==='doc'                 │                                  │
│  │    && action==='updated'          │                                  │
│  │    && id==='123'                  │                                  │
│  │    → __showStaleBanner()          │                                  │
│  │                                   │                                  │
│  │  [! Updated by another user  ][↺] │                                  │
│  └───────────────────────────────────┘                                  │
└──────────────────────────────────────────────────────────────────────────┘
              │  SSE signal merges            │  @get (on signal match)
              │  POST /sse/pong               │
              ▼                              ▼
┌──────────────────────────────────────────────────────────────────────────┐
│                                SERVER                                    │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │                       SSE Handler                                │   │
│  │  Parse + auth watch list from URL                                │   │
│  │  Register in fanout + heartbeat registry                         │   │
│  │  Emit dsEvent signal: {domain, id, action}                       │   │
│  │  Emit ssePing every 30s                                          │   │
│  │  defer: fanout.Unregister + registry.Unregister                  │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │                    Domain Fanout Manager                         │   │
│  │                                                                  │   │
│  │  Parses "doc.123.updated":                                       │   │
│  │    domain = "doc"                                                │   │
│  │    id     = "123"                                                │   │
│  │    action = "updated"                                            │   │
│  │                                                                  │   │
│  │  DomainFanout["doc"] — 1 bus subscriber forever                  │   │
│  │    conn-a: watch={"doc","doc.123"} → sends if domain or id match │   │
│  │    conn-b: watch={"doc","doc.456"} → sends if domain or id match │   │
│  │    conn-c: watch={"doc"}           → sends on domain match only  │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │                        Event Bus                                 │   │
│  │  Publish("doc.123.updated") → 1 channel send → lock released     │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │                    Fragment Endpoints                            │   │
│  │  GET /fragments/documents              auth → list templ         │   │
│  │  GET /fragments/documents/{id}         auth → drawer templ       │   │
│  │  GET /fragments/documents/{id}/row     auth → row templ          │   │
│  │  GET /fragments/documents/count        auth → count templ        │   │
│  │  GET /fragments/drawer/empty           → empty state             │   │
│  │  POST /sse/pong                        → 204                     │   │
│  └──────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Event Signature Parsing

The three-part signature `domain.id.action` is parsed at every layer:

```
  "doc.123.updated"
      │    │    │
      │    │    └── action : "updated" | "created" | "deleted"
      │    └─────── id     : entity ID as string
      └──────────── domain : entity type

  Parsing in Go:
  ──────────────────────────────────────────────────────────────
  parts  := strings.SplitN(sig, ".", 3)
  domain := parts[0]   // "doc"
  id     := parts[1]   // "123"
  action := parts[2]   // "updated"

  Watch element naming convention:
  ──────────────────────────────────────────────────────────────
  Broad:    <watch id="doc"/>        subscribes to all doc events
  Specific: <watch id="doc.123"/>    subscribes to doc:123 events only

  SSE URL:
  ──────────────────────────────────────────────────────────────
  /sse/events?watch=doc,doc.123

  dsEvent signal shape:
  ──────────────────────────────────────────────────────────────
  { domain: "doc", id: "123", action: "updated" }
```

---

## Lifecycle Flows

### Page Load

```
  t=0ms    GET /documents (auth-checked, full page)
           PageShell data-signals: {dsEvent:{}, sseConnID:"", ssePing:0}
           <watch id="doc"/> present

  t=1ms    DOMContentLoaded
           Worker: collectTopics() → {"doc"}
           GET /sse/events?watch=doc
           Server: auth ✓ → fanouts.Register → registry.Register
           SSE emits: {sseConnID:"uuid-abc"}

  t=5ms    #document-list data-on-load → @get('/fragments/documents')
           Server returns list with individual rows (each has data-on-signal-change)
           User sees list ✓
```

### Update Flows — Three Simultaneous Users

```
  Setup:
    User A: list open, drawer open on doc:123 (editable)
    User B: list open only
    User C: not on this page

  User C saves doc:123:
    bus.Publish("doc.123.updated")
          │
          ▼
    DomainFanout["doc"] dispatches:
      conn-A: watch={"doc","doc.123"} → hasDomain ✓ → sends
      conn-B: watch={"doc"}           → hasDomain ✓ → sends

    User A receives dsEvent{domain:"doc", id:"123", action:"updated"}
      #document-list: action=updated → does nothing ✓
      #doc-row-123:   action=updated, id=123 → @get .../123/row (debounced) ✓
      #document-drawer: action=updated, id=123 → __showStaleBanner() ✓
      User A sees: row title updates, banner appears in drawer
      User A's in-progress edits are untouched ✓

    User B receives dsEvent{domain:"doc", id:"123", action:"updated"}
      #document-list: action=updated → does nothing ✓
      #doc-row-123:   action=updated, id=123 → @get .../123/row (debounced) ✓
      User B sees: row title updates in place ✓

  User C creates doc:789:
    bus.Publish("doc.789.created")
          │
          ▼
    conn-A: sends, conn-B: sends

    Both users receive dsEvent{domain:"doc", id:"789", action:"created"}
      #document-list: action=created → @get('/fragments/documents') ✓
      All rows: id=789 !== their own id → nothing
      Both users see the new document appear in the list ✓

  User C deletes doc:456:
    bus.Publish("doc.456.deleted")
          │
          ▼
    dsEvent{domain:"doc", id:"456", action:"deleted"}
      #document-list: action=deleted → @get('/fragments/documents') ✓
      #doc-row-456:   action=deleted, id=456 → @get .../456/row
        Server returns 404 or empty → row cleared ✓
      List re-fetches and doc:456 is gone ✓
```

### Opening a Drawer

```
  User clicks doc:123 row
          │
          ▼
  data-on-click: @get('/fragments/documents/123')
  Server: auth.Can(user, "doc:read", "123") ✓
  Server renders DocumentDrawerEditable(doc) containing:
    <watch id="doc.123"/>
    data-on-signal-change__window: domain=doc && action=updated && id=123 → banner
    data-on-signal-change__window: $sseConnID && @get('/fragments/documents/123')
          │
          ▼
  Fragment merges into #document-drawer
  MutationObserver: <watch id="doc.123"/> detected
  Worker: collectTopics() → {"doc", "doc.123"}
  Debounce 300ms → GET /sse/events?watch=doc,doc.123
  Server: re-auth both ✓
  fanouts.Register(connID, ["doc","doc.123"], merged)
          │
          ▼
  $sseConnID changes (new connID emitted on reconnect)
  data-on-signal-change__window: $sseConnID fires
  @get('/fragments/documents/123') → fresh render
  Any edit that happened during reconnect gap is recovered ✓
```

### Closing the Drawer

```
  User clicks Close
          │
          ▼
  @get('/fragments/drawer/empty')
  DrawerEmpty: no <watch>, no data-on-signal-change
  <watch id="doc.123"/> removed from DOM
          │
          ▼
  MutationObserver fires
  Worker: collectTopics() → {"doc"}
  GET /sse/events?watch=doc
  doc.123 subscription gone ✓
  No stale banner, no signal listener — component fully unmounted ✓
```

### Reconnect Gap Recovery

```
  Drawer opens on doc:123
  <watch id="doc.123"/> added to DOM
  Worker debounces 300ms → reconnects SSE
          │
  During 300ms gap: collaborator saves doc:123
          │
  SSE reconnects
  Server emits new sseConnID
  $sseConnID signal changes
          │
  data-on-signal-change__window in drawer:
    $sseConnID && @get('/fragments/documents/123')
  Fires immediately → fresh render ✓
  Missed edit recovered ✓
```

---

## Security Model

```
  ID flow — server gives, client reflects:
  ──────────────────────────────────────────────────────────────────────
  Server: auth.Can(user, "doc:read", "123") ✓
  Server renders fragment with <watch id="doc.123"/>
  Client DOM reflects "doc.123"
  Worker sends: GET /sse/events?watch=doc,doc.123
  Server re-auth-checks "doc.123" ✓

  Malicious injection: GET /sse/events?watch=doc,doc.999
  parseAndAuthorize:
    "doc.999" → auth.Can(user, "doc:read", "999") ✗ → silently dropped
  fanouts.Register(connID, ["doc"], merged)
  doc.999 signals never sent to this client ✓
  Silence is indistinguishable from "does not exist" ✓

  Defence in depth — auth at every layer:
  ──────────────────────────────────────────────────────────────────────
  GET /fragments/documents/123   → auth.Can checked → 403 if denied
  GET /sse/events?watch=doc.123  → auth.Can checked → topic dropped if denied
  dsEvent fires → @get /123      → auth.Can checked → 403 if access revoked
```

---

## Project Structure

```
  yourapp/
  ├── cmd/server/main.go
  ├── internal/
  │   ├── bus/bus.go              event bus — Publish(sig string)
  │   ├── fanout/fanout.go        DomainFanout + Manager
  │   ├── sse/
  │   │   ├── handler.go          EventStream + Pong
  │   │   └── registry.go         heartbeat registry + reaper
  │   ├── handler/fragments.go    all fragment endpoints
  │   ├── store/documents.go      data access
  │   ├── auth/auth.go            authorization
  │   └── service/documents.go    business logic + bus.Publish
  ├── views/
  │   ├── shell.templ             PageShell: signals, <watch>, pong handler
  │   ├── documents.templ         list container, row, drawer variants, empty
  │   └── layout.templ            base HTML
  └── static/watch-worker.js      MutationObserver + SSE manager (~80 lines)
```

---

## Full Implementation

### Event Bus

```go
// internal/bus/bus.go
package bus

import (
    "strings"
    "sync"
)

// Event carries the parsed three-part signature.
// Raw is the original "doc.123.updated" string — passed through to the SSE
// signal so the client receives all three parts without re-joining.
type Event struct {
    Raw    string // "doc.123.updated"
    Domain string // "doc"
    ID     string // "123"
    Action string // "updated" | "created" | "deleted"
}

// ParseEvent parses "doc.123.updated" into its three parts.
func ParseEvent(sig string) Event {
    parts := strings.SplitN(sig, ".", 3)
    if len(parts) != 3 {
        return Event{Raw: sig}
    }
    return Event{Raw: sig, Domain: parts[0], ID: parts[1], Action: parts[2]}
}

type Bus struct {
    mu   sync.RWMutex
    subs map[string][]chan Event // keyed by domain only: "doc"
}

func New() *Bus {
    return &Bus{subs: make(map[string][]chan Event)}
}

func (b *Bus) Subscribe(domain string) (<-chan Event, func()) {
    ch := make(chan Event, 16)
    b.mu.Lock()
    b.subs[domain] = append(b.subs[domain], ch)
    b.mu.Unlock()

    return ch, func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        subs := b.subs[domain]
        for i, s := range subs {
            if s == ch {
                b.subs[domain] = append(subs[:i], subs[i+1:]...)
                close(ch)
                return
            }
        }
    }
}

// Publish accepts the full signature "doc.123.updated".
// It routes to the domain's fanout goroutine via a single channel send.
// The bus lock is held for exactly 1 send regardless of connection count.
func (b *Bus) Publish(sig string) {
    evt := ParseEvent(sig)
    if evt.Domain == "" {
        return
    }
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs[evt.Domain] {
        select {
        case ch <- evt:
        default:
        }
    }
}
```

### Domain Fanout

```go
// internal/fanout/fanout.go
package fanout

import (
    "strings"
    "sync"

    "yourapp/internal/bus"
)

type connEntry struct {
    watch map[string]struct{} // {"doc", "doc.123"}
    ch    chan bus.Event
}

// DomainFanout subscribes once to a domain and fans events to
// matching connections. One instance per domain, forever.
type DomainFanout struct {
    mu    sync.RWMutex
    conns map[string]*connEntry
}

func newDomainFanout() *DomainFanout {
    return &DomainFanout{conns: make(map[string]*connEntry)}
}

func (f *DomainFanout) register(connID string, topics []string, ch chan bus.Event) {
    watch := make(map[string]struct{}, len(topics))
    for _, t := range topics {
        watch[t] = struct{}{}
    }
    f.mu.Lock()
    f.conns[connID] = &connEntry{watch: watch, ch: ch}
    f.mu.Unlock()
}

func (f *DomainFanout) unregister(connID string) {
    f.mu.Lock()
    delete(f.conns, connID)
    f.mu.Unlock()
}

// dispatch fans the event to connections that match either:
//   hasBroad:    watch contains "doc"       → receives all doc events
//   hasSpecific: watch contains "doc.123"   → receives doc.123 events
//
// The action field is NOT used for routing — it is forwarded to the client
// where Data-Star expressions filter by action per component.
func (f *DomainFanout) dispatch(evt bus.Event) {
    specific := evt.Domain + "." + evt.ID // "doc.123"

    f.mu.RLock()
    defer f.mu.RUnlock()

    for _, entry := range f.conns {
        _, hasBroad    := entry.watch[evt.Domain]
        _, hasSpecific := entry.watch[specific]
        if hasBroad || hasSpecific {
            select {
            case entry.ch <- evt:
            default:
            }
        }
    }
}

// Manager owns one DomainFanout per entity domain.
type Manager struct {
    bus     *bus.Bus
    mu      sync.Mutex
    fanouts map[string]*DomainFanout
}

func NewManager(b *bus.Bus) *Manager {
    return &Manager{bus: b, fanouts: make(map[string]*DomainFanout)}
}

func (m *Manager) Register(connID string, topics []string, ch chan bus.Event) {
    for _, domain := range extractDomains(topics) {
        m.getOrCreate(domain).register(connID, topics, ch)
    }
}

func (m *Manager) Unregister(connID string, topics []string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    for _, domain := range extractDomains(topics) {
        if f, ok := m.fanouts[domain]; ok {
            f.unregister(connID)
        }
    }
}

func (m *Manager) getOrCreate(domain string) *DomainFanout {
    m.mu.Lock()
    defer m.mu.Unlock()
    if f, ok := m.fanouts[domain]; ok {
        return f
    }
    f := newDomainFanout()
    m.fanouts[domain] = f
    events, _ := m.bus.Subscribe(domain)
    go func() {
        for evt := range events {
            f.dispatch(evt)
        }
    }()
    return f
}

// extractDomains derives unique domain names from a topic list.
//   "doc"     → "doc"
//   "doc.123" → "doc"
func extractDomains(topics []string) []string {
    seen := make(map[string]struct{})
    for _, t := range topics {
        domain := t
        if i := strings.Index(t, "."); i != -1 {
            domain = t[:i]
        }
        seen[domain] = struct{}{}
    }
    out := make([]string, 0, len(seen))
    for d := range seen {
        out = append(out, d)
    }
    return out
}
```

### Heartbeat Registry

```go
// internal/sse/registry.go
package sse

import (
    "context"
    "sync"
    "time"
)

type conn struct {
    cancel  context.CancelFunc
    lastACK time.Time
}

type Registry struct {
    mu    sync.Mutex
    conns map[string]*conn
}

func NewRegistry() *Registry {
    return &Registry{conns: make(map[string]*conn)}
}

func (r *Registry) Register(id string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.conns[id] = &conn{cancel: cancel, lastACK: time.Now()}
}

func (r *Registry) Pong(id string) bool {
    r.mu.Lock()
    defer r.mu.Unlock()
    c, ok := r.conns[id]
    if !ok {
        return false
    }
    c.lastACK = time.Now()
    return true
}

func (r *Registry) Unregister(id string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    delete(r.conns, id)
}

func (r *Registry) Reaper(deadline time.Duration) {
    ticker := time.NewTicker(deadline / 2)
    defer ticker.Stop()
    for range ticker.C {
        r.mu.Lock()
        for id, c := range r.conns {
            if time.Since(c.lastACK) > deadline {
                c.cancel()
                delete(r.conns, id)
            }
        }
        r.mu.Unlock()
    }
}
```

### SSE Handler

```go
// internal/sse/handler.go
package sse

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "strings"
    "time"

    datastar "github.com/starfederation/datastar/sdk/go"
    "yourapp/internal/auth"
    "yourapp/internal/bus"
    "yourapp/internal/fanout"
)

const (
    PingInterval = 30 * time.Second
    PongDeadline = 45 * time.Second
)

type Handler struct {
    fanouts  *fanout.Manager
    registry *Registry
    auth     *auth.Auth
}

func NewHandler(f *fanout.Manager, r *Registry, a *auth.Auth) *Handler {
    return &Handler{fanouts: f, registry: r, auth: a}
}

func (h *Handler) EventStream(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithCancel(r.Context())
    defer cancel()

    connID := fmt.Sprintf("%d", time.Now().UnixNano())
    h.registry.Register(connID, cancel)
    defer h.registry.Unregister(connID)

    sse, err := datastar.NewSSE(w, r.WithContext(ctx))
    if err != nil {
        return
    }

    topics := h.parseAndAuthorize(r)
    if len(topics) == 0 {
        return
    }

    merged := make(chan bus.Event, 32)
    h.fanouts.Register(connID, topics, merged)
    defer h.fanouts.Unregister(connID, topics)

    ping := time.NewTicker(PingInterval)
    defer ping.Stop()

    if err := sse.MarshalAndMergeSignals(map[string]any{
        "sseConnID": connID,
    }); err != nil {
        return
    }

    for {
        select {
        case <-ctx.Done():
            return

        case <-ping.C:
            if err := sse.MarshalAndMergeSignals(map[string]any{
                "ssePing": time.Now().UnixMilli(),
            }); err != nil {
                return
            }

        case evt, ok := <-merged:
            if !ok {
                return
            }
            // Forward all three parts of the signature as separate signal fields.
            // Components filter by domain, id, and action independently.
            if err := sse.MarshalAndMergeSignals(map[string]any{
                "dsEvent": map[string]any{
                    "domain": evt.Domain,
                    "id":     evt.ID,
                    "action": evt.Action,
                },
            }); err != nil {
                return
            }
        }
    }
}

func (h *Handler) Pong(w http.ResponseWriter, r *http.Request) {
    var body struct {
        ConnID string `json:"connID"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        return
    }
    if !h.registry.Pong(body.ConnID) {
        w.WriteHeader(http.StatusGone)
        return
    }
    w.WriteHeader(http.StatusNoContent)
}

// parseAndAuthorize parses topic strings and drops any the user cannot access.
// Topics use dot notation: "doc" or "doc.123"
// Unauthorized topics are silently dropped — no error, no existence leak.
func (h *Handler) parseAndAuthorize(r *http.Request) []string {
    user    := auth.UserFromCtx(r.Context())
    raw     := strings.Split(r.URL.Query().Get("watch"), ",")
    allowed := make([]string, 0, len(raw))

    for _, topic := range raw {
        topic = strings.TrimSpace(topic)
        if topic == "" {
            continue
        }
        switch {
        case topic == "doc":
            if h.auth.Can(user, "doc:list") {
                allowed = append(allowed, topic)
            }
        case strings.HasPrefix(topic, "doc."):
            id := strings.TrimPrefix(topic, "doc.")
            if h.auth.Can(user, "doc:read", id) {
                allowed = append(allowed, topic)
            }
        case topic == "invoice":
            if h.auth.Can(user, "invoice:list") {
                allowed = append(allowed, topic)
            }
        }
    }
    return allowed
}
```

### Fragment Handler

```go
// internal/handler/fragments.go
package handler

import (
    "net/http"
    "strconv"

    "github.com/a-h/templ"
    "yourapp/internal/auth"
    "yourapp/internal/store"
    "yourapp/views"
)

type FragmentHandler struct {
    store *store.Store
    auth  *auth.Auth
}

func (h *FragmentHandler) DocumentList(w http.ResponseWriter, r *http.Request) {
    user := auth.UserFromCtx(r.Context())
    if !h.auth.Can(user, "doc:list") {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }
    docs, _ := h.store.ListDocuments(r.Context(), user)
    templ.Handler(views.DocumentList(docs)).ServeHTTP(w, r)
}

func (h *FragmentHandler) DocumentRow(w http.ResponseWriter, r *http.Request) {
    user  := auth.UserFromCtx(r.Context())
    idStr := r.PathValue("id")
    id, _ := strconv.ParseInt(idStr, 10, 64)
    if !h.auth.Can(user, "doc:read", idStr) {
        // Return empty row rather than 403 — 403 would break Data-Star merge
        templ.Handler(views.DocumentRowEmpty(idStr)).ServeHTTP(w, r)
        return
    }
    doc, err := h.store.GetDocument(r.Context(), id)
    if err != nil {
        templ.Handler(views.DocumentRowEmpty(idStr)).ServeHTTP(w, r)
        return
    }
    templ.Handler(views.DocumentRow(doc)).ServeHTTP(w, r)
}

func (h *FragmentHandler) DocumentDetail(w http.ResponseWriter, r *http.Request) {
    user  := auth.UserFromCtx(r.Context())
    idStr := r.PathValue("id")
    id, _ := strconv.ParseInt(idStr, 10, 64)
    if !h.auth.Can(user, "doc:read", idStr) {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }
    doc, err := h.store.GetDocument(r.Context(), id)
    if err != nil {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }
    templ.Handler(views.DocumentDrawerEditable(doc)).ServeHTTP(w, r)
}

func (h *FragmentHandler) DrawerEmpty(w http.ResponseWriter, r *http.Request) {
    templ.Handler(views.DrawerEmpty()).ServeHTTP(w, r)
}
```

### Service Layer

```go
// internal/service/documents.go
package service

import (
    "context"
    "yourapp/internal/bus"
    "yourapp/internal/store"
)

type DocumentService struct {
    store *store.Store
    bus   *bus.Bus
}

func (s *DocumentService) CreateDocument(ctx context.Context, input CreateInput) (int64, error) {
    id, err := s.store.Create(ctx, input)
    if err != nil {
        return 0, err
    }
    s.bus.Publish(fmt.Sprintf("doc.%d.created", id))
    return id, nil
}

func (s *DocumentService) UpdateDocument(ctx context.Context, id int64, input UpdateInput) error {
    if err := s.store.Update(ctx, id, input); err != nil {
        return err
    }
    s.bus.Publish(fmt.Sprintf("doc.%d.updated", id))
    return nil
}

func (s *DocumentService) DeleteDocument(ctx context.Context, id int64) error {
    if err := s.store.Delete(ctx, id); err != nil {
        return err
    }
    s.bus.Publish(fmt.Sprintf("doc.%d.deleted", id))
    return nil
}
```

### Templ Views

```go
// views/shell.templ
package views

templ PageShell() {
    <!DOCTYPE html>
    <html lang="en">
    <head>
        <meta charset="UTF-8"/>
        <script type="module"
            src="https://cdn.jsdelivr.net/npm/@sudodevnull/datastar">
        </script>
        <script src="/static/watch-worker.js"></script>
        <script>
        window.__showStaleBanner = function() {
            const el = document.getElementById('drawer-stale-banner')
            if (el) el.style.display = 'flex'
        }
        </script>
    </head>
    <body data-signals='{"dsEvent":{}, "sseConnID":"", "ssePing":0}'>

        // Broad topic — always present
        <watch id="doc"></watch>

        // Heartbeat pong — pure Data-Star, no JS required
        <div
            data-on-signal-change__window={`
                $ssePing && @post('/sse/pong', {connID: $sseConnID})
            `}
        ></div>

        <main>
            @DocumentListComponent()
            <div id="document-drawer"></div>
        </main>
    </body>
    </html>
}
```

```go
// views/documents.templ
package views

import (
    "fmt"
    "strconv"
    "yourapp/internal/model"
)

// DocumentListComponent — reacts to created/deleted only.
// Updated events are handled by individual rows.
templ DocumentListComponent() {
    <div
        id="document-list"
        data-on-load="@get('/fragments/documents')"
        data-on-signal-change__window={`
            $dsEvent?.domain === 'doc'
            && ($dsEvent?.action === 'created' || $dsEvent?.action === 'deleted')
            && @get('/fragments/documents')
        `}
    >
        <p>Loading...</p>
    </div>
}

// DocumentList — server-rendered list containing individual rows.
templ DocumentList(docs []model.Document) {
    <div id="document-list">
        for _, doc := range docs {
            @DocumentRow(doc)
        }
        if len(docs) == 0 {
            <p>No documents.</p>
        }
    </div>
}

// DocumentRow — reacts to updated events for its own ID only, debounced.
// Each row is an independently replaceable fragment target.
templ DocumentRow(doc model.Document) {
    <div
        id={fmt.Sprintf("doc-row-%d", doc.ID)}
        class="doc-row"
        data-on-click={fmt.Sprintf(`@get('/fragments/documents/%d')`, doc.ID)}
        data-on-signal-change__window.debounce_500ms={fmt.Sprintf(`
            $dsEvent?.domain === 'doc'
            && $dsEvent?.action === 'updated'
            && $dsEvent?.id === '%d'
            && @get('/fragments/documents/%d/row')
        `, doc.ID, doc.ID)}
    >
        <span class="title">{ doc.Title }</span>
        <span class="time">{ doc.UpdatedAt.Format("15:04:05") }</span>
    </div>
}

// DocumentRowEmpty — returned when a row is deleted or access is revoked.
// Renders as empty so Data-Star has a valid merge target.
templ DocumentRowEmpty(id string) {
    <div id={fmt.Sprintf("doc-row-%s", id)} style="display:none"></div>
}

// DocumentDrawerEditable — stale banner pattern for content the user may edit.
// Auto-reload would destroy in-progress work.
// Re-fetches on reconnect to close the event gap.
templ DocumentDrawerEditable(doc model.Document) {
    <div id="document-drawer">

        // Subscription declaration — presence = watching doc.N
        <watch id={fmt.Sprintf("doc.%d", doc.ID)}></watch>

        // Stale banner — appears when a remote update arrives.
        // Does not replace the drawer content, only shows a notification.
        <div
            id="drawer-stale-banner"
            style="display:none"
            data-on-signal-change__window={fmt.Sprintf(`
                $dsEvent?.domain === 'doc'
                && $dsEvent?.action === 'updated'
                && $dsEvent?.id === '%d'
                && __showStaleBanner()
            `, doc.ID)}
        >
            <span>Updated by another user.</span>
            <button
                data-on-click={fmt.Sprintf(
                    `@get('/fragments/documents/%d')`, doc.ID,
                )}
            >Load latest</button>
            <button
                onclick="document.getElementById('drawer-stale-banner').style.display='none'"
            >Dismiss</button>
        </div>

        // Re-fetch on reconnect to recover any event missed during the gap.
        // $sseConnID changes every time the SSE connection is re-established.
        <div
            data-on-signal-change__window={fmt.Sprintf(`
                $sseConnID && @get('/fragments/documents/%d')
            `, doc.ID)}
            style="display:none"
        ></div>

        <div class="drawer-header">
            <h2>{ doc.Title }</h2>
            <button data-on-click="@get('/fragments/drawer/empty')">Close</button>
        </div>
        <textarea>{ doc.Body }</textarea>
    </div>
}

// DrawerEmpty — no <watch>, no data-on-signal-change.
// Worker detects absence of <watch id="doc.N"/> and reconnects.
templ DrawerEmpty() {
    <div id="document-drawer">
        <p>Select a document.</p>
    </div>
}
```

### Watch Worker

```javascript
// static/watch-worker.js
(function () {
    'use strict'

    let currentTopics = new Set()
    let sseConn       = null
    let connID        = null
    let debounceTimer = null

    function collectTopics() {
        return new Set(
            [...document.querySelectorAll('watch[id]')]
                .map(el => el.getAttribute('id'))
        )
    }

    function setsEqual(a, b) {
        if (a.size !== b.size) return false
        for (const v of a) if (!b.has(v)) return false
        return true
    }

    function connect(topics) {
        if (sseConn) {
            sseConn.close()
            sseConn = null
            connID  = null
        }
        if (topics.size === 0) return

        const url = '/sse/events?watch=' + [...topics].join(',')
        sseConn       = new EventSource(url)
        currentTopics = new Set(topics)

        sseConn.addEventListener('message', (e) => {
            let data
            try { data = JSON.parse(e.data) } catch { return }

            // sseConnID and ssePing are managed here.
            // dsEvent is merged into Data-Star signals automatically by
            // the Data-Star SSE processor — components react via
            // data-on-signal-change__window without any JS here.
            if (data.sseConnID) connID = data.sseConnID

            if (data.ssePing && connID) {
                fetch('/sse/pong', {
                    method:  'POST',
                    headers: {'Content-Type': 'application/json'},
                    body:    JSON.stringify({connID}),
                }).catch(() => {})
            }
        })

        sseConn.addEventListener('error', () => { connID = null })

        sseConn.addEventListener('open', () => {
            const live = collectTopics()
            if (!setsEqual(live, currentTopics)) connect(live)
        })
    }

    function isWatchMutation(mutations) {
        return mutations.some(m =>
            [...m.addedNodes, ...m.removedNodes].some(n => {
                if (n.nodeType !== Node.ELEMENT_NODE) return false
                if (n.nodeName === 'WATCH') return true
                return n.querySelectorAll?.('watch[id]').length > 0
            })
        )
    }

    const observer = new MutationObserver((mutations) => {
        if (!isWatchMutation(mutations)) return
        clearTimeout(debounceTimer)
        debounceTimer = setTimeout(() => {
            const next = collectTopics()
            if (!setsEqual(next, currentTopics)) connect(next)
        }, 300)
    })

    document.addEventListener('DOMContentLoaded', () => {
        observer.observe(document.body, {childList: true, subtree: true})
        connect(collectTopics())
    })
})()
```

### Router

```go
// cmd/server/main.go
package main

import (
    "net/http"
    "yourapp/internal/auth"
    "yourapp/internal/bus"
    "yourapp/internal/fanout"
    "yourapp/internal/handler"
    "yourapp/internal/service"
    "yourapp/internal/sse"
    "yourapp/internal/store"
)

func main() {
    db    := connectDB()
    b     := bus.New()
    fm    := fanout.NewManager(b)
    reg   := sse.NewRegistry()
    authz := auth.New(db)
    st    := store.New(db)
    _      = service.New(st, b)

    sseH  := sse.NewHandler(fm, reg, authz)
    fragH := handler.NewFragmentHandler(st, authz)

    go reg.Reaper(sse.PongDeadline)

    mux := http.NewServeMux()
    mux.HandleFunc("GET /sse/events",                   sseH.EventStream)
    mux.HandleFunc("POST /sse/pong",                    sseH.Pong)
    mux.HandleFunc("GET /fragments/documents",          fragH.DocumentList)
    mux.HandleFunc("GET /fragments/documents/{id}",     fragH.DocumentDetail)
    mux.HandleFunc("GET /fragments/documents/{id}/row", fragH.DocumentRow)
    mux.HandleFunc("GET /fragments/drawer/empty",       fragH.DrawerEmpty)
    mux.HandleFunc("GET /documents",                    handler.DocumentsPage)
    mux.Handle("GET /static/",
        http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

    http.ListenAndServe(":8080", authMiddleware(mux))
}
```

---

## Endpoints Reference

```
  Method  Path                              Auth          Purpose
  ───────────────────────────────────────────────────────────────────────
  GET     /documents                        session       Full page
  GET     /sse/events?watch=...             per-topic     Signal stream
  POST    /sse/pong                         none          Heartbeat ACK
  GET     /fragments/documents              doc:list      Full list fragment
  GET     /fragments/documents/{id}         doc:read:{id} Drawer fragment
  GET     /fragments/documents/{id}/row     doc:read:{id} Single row fragment
  GET     /fragments/drawer/empty           none          Empty drawer
  GET     /static/watch-worker.js           none          Worker script
```

---

## Component Contract

```
  ┌────────────────────────────────────────────────────────────────────┐
  │                       Component Contract                           │
  │                                                                    │
  │  DECLARE subscription:                                             │
  │    <watch id="domain.id"/>  (specific)                             │
  │    <watch id="domain"/>     (broad — in PageShell)                 │
  │                                                                    │
  │  REACT using action-aware expression:                              │
  │    data-on-signal-change__window="                                 │
  │      $dsEvent?.domain === 'X'                                      │
  │      && $dsEvent?.action === 'updated'   (or created/deleted)      │
  │      && $dsEvent?.id === 'N'             (for instance components) │
  │      && @get('/fragments/...')"                                    │
  │                                                                    │
  │  CHOOSE the right pattern for this component's content:            │
  │    user may have edits       → stale banner, not auto-reload       │
  │    read-only content         → auto-reload + reconnect guard       │
  │    list structure            → react to created/deleted only       │
  │    list row                  → react to updated, debounced         │
  │    high-frequency events     → debounce the expression             │
  │                                                                    │
  │  CLEAN UP:                                                         │
  │    Nothing. Remove the component from the DOM.                     │
  │    <watch> and data-on-signal-change leave with it.                │
  └────────────────────────────────────────────────────────────────────┘
```

---

## Failure Modes

```
  Failure                    Behaviour                      Recovery
  ──────────────────────────────────────────────────────────────────────────
  Client crash               Ghost conn ≤45s                Reaper + fanout cleanup
  Network partition          Ghost conn ≤45s                Same
  Server restart             All SSEs drop                  Worker reconnects from DOM
                                                            $sseConnID triggers refetch
  Event during reconnect gap Drawer shows stale data        $sseConnID guard refetches
  Re-fetch storm (autosave)  Multiple @get in flight        debounce_500ms absorbs it
  Out-of-order responses     Stale render wins              debounce + Data-Star merge
                                                            order prevents this
  Fragment 403               Component stays stale          Correct — access revoked
  Fragment 500               Component stays stale          Retries on next signal
  Worker script fails        No real-time updates           Page still functional
  <watch> ID typo            Subscribed, signal never fires Silent — no side effects
  deleted row row-fragment   Empty div returned             Row hidden, list refetches
```

---

## Observability

```
  Metric                        Alert condition               Meaning
  ──────────────────────────────────────────────────────────────────────────
  bus_subscribers_total          > domains × 2                Bus leak
  fanout_conns_per_domain        > sse_connections_active      Unregister not firing
  fanout_dispatch_duration_p99   > 10ms                       Fanout needs sharding
  sse_connections_active         > 2× expected users          Reaper failure
  sse_pong_latency_p99           > 5s                         Client degraded
  fragment_requests_total        ≫ event_rate × connections   Missing debounce

  bus_subscribers_total should always equal the number of entity domains.
  If it scales with connected users the fanout is not being used correctly.
```

---

## Heartbeat Timing

```
  PingInterval  PongDeadline  Ghost Lifetime  Pong req/hr (1k users)
  ──────────────────────────────────────────────────────────────────
  10s           15s           ≤15s            360k  ← high churn
  30s           45s           ≤45s            120k  ← recommended
  60s           90s           ≤90s             60k  ← mobile / battery
```

Reaper interval = `PongDeadline / 2`.

---

## Scaling

### Multiple Processes

Replace the in-process bus with NATS JetStream or Redis Pub/Sub. NATS subjects
map directly to the `domain` field: `nats.Publish("doc", payload)`. The fanout
manager, SSE handler, registry, and all views are unchanged.

```
  Single process:
  bus.Publish("doc.123.updated") → fanout["doc"] → matching conns

  Multi-process (NATS):
  bus.Publish("doc.123.updated") → NATS subject "doc"
                                        │
                          ┌────────────┴────────────┐
                          ▼                          ▼
                    process A                  process B
                    NATS.Subscribe("doc")      NATS.Subscribe("doc")
                    → fanout["doc"]            → fanout["doc"]
                    → local conn fan-out       → local conn fan-out
```

### Sharding the Fanout

If `fanout_dispatch_duration_p99` exceeds 10ms under load, partition `conns`
by connection ID hash across N goroutines, each with its own lock.
Implement only with measurement evidence.

---

## Consequences

### Positive

- **Action-aware routing eliminates whole-list re-renders** — lists only re-fetch on structural changes; rows update in place; drawers show banners rather than losing user state
- **Cleanup is automatic** — DOM removal is the unsubscription; leaks are structurally impossible
- **Constant bus subscriber count** — O(domains), not O(connections × topics); bus lock held for 1 send
- **Reconnect gap is closed** — `$sseConnID` guard triggers re-fetch after every reconnect
- **Debounce absorbs rapid events** — autosave storms collapse to one re-fetch at the row level
- **Stateless server** — rolling deploys and restarts need zero recovery logic
- **Graceful degradation** — worker failure leaves the page functional; real-time simply stops

### Negative

- **More fragment endpoints** — list, row, drawer, and count are separate endpoints; more surface to maintain
- **Action field must be consistent** — every `bus.Publish` call must use the correct action; a missing or wrong action causes silent missed updates
- **`$sseConnID` guard fires on every reconnect** — every drawer re-fetches on reconnect regardless of whether anything changed; acceptable cost for correctness
- **Stale banner requires product decision per component** — developers must consciously choose the right pattern; defaulting to auto-reload is always wrong for editable content
- **URL length ceiling** — ~50 simultaneously open instance-level `<watch>` elements approaches query string limits; switch SSE to POST body to mitigate

---

## Review Triggers

- `fanout_dispatch_duration_p99` exceeds 10ms → shard the fanout
- Pages exceed ~50 open `<watch>` elements → switch SSE to POST body
- Sub-150ms latency required → consider WebSocket + server-side diff
- Multi-process deployment → swap bus transport only
- New action types needed (e.g. `locked`, `published`) → extend `bus.Event` and add component patterns to this ADR
