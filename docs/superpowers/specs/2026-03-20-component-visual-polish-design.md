# Component Visual Polish — Design Spec

**Date:** 2026-03-20
**Scope:** All 69+ UI components in `ui/`
**Approach:** Category-by-category sweep, freely changing defaults
**Constraint:** No custom CSS/JS — DaisyUI classes + Tailwind utilities only

## Problem

Components pass through DaisyUI's base classes without additional Tailwind polish. Elevated surfaces lack depth, custom focusable elements miss keyboard-accessible focus rings, and interactive containers don't respond to hover.

## Changes by Category

### Group 1 — Buttons & Inputs

Components: `button`, `toggle`, `radio`, `textarea`, `selectinput`, `rating`, `rangeinput`, `moneyinput`, `fileinput`, `fileupload`

DaisyUI handles hover/focus states for native `<button>` and `<input>` elements via the `.btn:focus-visible` CSS rule (applies regardless of element type). No changes needed.

### Group 2 — Cards & Containers

Components: `card`, `fieldset`, `navbar`, `footer`, `label`

| Component | Current | Change |
|-----------|---------|--------|
| Card | `bg-base-200 shadow-sm` | `bg-base-200 shadow-md` |
| Navbar | structural only | No change |
| Footer | structural only | No change |
| Fieldset | structural only | No change |
| Label | structural only | No change |

Note: `transition-shadow` is omitted because there is no hover state triggering a shadow change within the component itself. Consumers can add `hover:shadow-lg transition-shadow` via the `Class` prop.

### Group 3 — Data Display

Components: `badge`, `stat`, `table`, `avatar`, `indicator`, `kbd`, `progress`, `radialprogress`, `skeleton`, `sparkline`, `codeview`, `jsonview`, `yamltree`, `markdown`, `status`, `money`, `icon`

Display-only components — DaisyUI handles styling. No changes.

Note: `list` already uses `shadow-md`, which is consistent with the Card upgrade. No change needed.

### Group 4 — Feedback

Components: `alert`, `toast`, `loading`, `tooltip`

No changes. DaisyUI handles alert appearance, tooltip animation, and loading indicators.

### Group 5 — Navigation

Components: `tabs`, `breadcrumbs`, `menu`, `pagination`, `steps`, `timeline`, `dock`, `link`

| Component | Current | Change |
|-----------|---------|--------|
| Tab Content | `tab-content` | `tab-content p-4` (default padding for content panels) |
| Others | structural | No change |

Note: Adding `p-4` to tab content is a visual default change. Existing usage relying on zero padding will see a layout shift. Consumers can override via `Class` prop since `TwMerge` is used. This is acceptable per the "freely change defaults" approach.

### Group 6 — Interactive/Overlay

Components: `modal`, `dropdown`, `drawer`, `accordion`, `form`, `calendar`, `commandbar`, `filter`

| Component | Current | Change |
|-----------|---------|--------|
| Dropdown Content | `dropdown-content menu bg-base-100 rounded-box z-1 w-52 p-2 shadow-sm` | `dropdown-content menu bg-base-100 rounded-box z-1 w-52 p-2 shadow-lg border border-base-200` |
| Modal Box | `modal-box` | `modal-box shadow-xl` |
| Accordion Item | `collapse {modifier} bg-base-100 border border-base-300` | `collapse {modifier} bg-base-100 border border-base-300 transition-all duration-200` |
| Accordion Title | `collapse-title font-semibold cursor-pointer` | `collapse-title font-semibold cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50` |

Note: Dropdown trigger focus ring removed — DaisyUI's `.btn:focus-visible` CSS applies to `<div>` elements with the `btn` class, so adding an explicit ring would double up. Accordion title is NOT a DaisyUI `btn`, so it needs an explicit focus-visible ring. The `rounded` class is omitted to avoid radius mismatch with the parent collapse container.

### Group 7 — Layout & Misc

Components: `join`, `separator`, `stack`, `scrollstrip`, `list`, `carousel`, `hovergallery`, `fab`, `mockupcode`

Structural utilities or already styled — no visual changes.

### Group 8 — Composite/Application

Components: `aichat`, `briefing`, `chat`, `feed`, `feeditem`, `themecontroller`, `validator`, `textrotate`

Application-level composites. DaisyUI and their internal structure handle styling. No changes.

## Summary of All Changes

1. **Card** — stronger shadow (`shadow-sm` → `shadow-md`)
2. **Dropdown content** — elevated shadow (`shadow-lg`), subtle border (`border border-base-200`)
3. **Modal box** — elevated shadow (`shadow-xl`) for proper overlay depth
4. **Accordion item** — smooth transition (`transition-all duration-200`) for open/close
5. **Accordion title** — focus-visible ring for keyboard accessibility
6. **Tab content** — default padding (`p-4`)

Total: 5 component files changed, 6 specific class modifications.

## Files Modified

- `ui/card/card.templ`
- `ui/dropdown/dropdown.templ`
- `ui/modal/modal.templ`
- `ui/accordion/accordion.templ`
- `ui/tab/tab.templ`

## Out of Scope

- No API changes (Props structs unchanged)
- No new variants or sizes
- No showcase changes
- No custom CSS or JS
