# Design direction

The interface a new screen has to fit. Every value below is the one the product already
ships; a new screen reads this instead of inventing a second language.

## Palette

Light theme, from `ui/leafwiki-ui/src/index.css`:

| Role | Token | Value |
|---|---|---|
| Page | `--background` | `0 0% 100%` |
| Panel | `--surface` | `210 40% 98%` |
| Panel, second level | `--surface-alt` | `210 40% 96%` |
| Border | `--surface-border` | `214 32% 91%` |
| Text | `--foreground` | `222 47% 11%` |
| Interface text | `--interface-text` | `215 25% 27%` |
| Muted text | `--muted` | `215 16% 47%` |
| Accent | `--brand` | `142 72% 29%` |

One accent, used for primary actions and for the one thing on screen that matters most. No
gradients, no glows, no glassmorphism: the surfaces are flat and the borders carry the
structure.

## Type

- Family: `Inter` with the system stack behind it (`--font-sans`), plus the platform's CJK
  fallback. No display face, no monospace except for code.
- Scale in use: page title 24px bold, section title 18px semibold, body 14px, hint 12px.
- Text is left aligned; nothing is centred except a deliberately empty state.

## Shape and space

- Radius: 12px for the page-level surface (`--radius`), 8px for panels (`--radius-lg`),
  6px inside them (`--radius-md`).
- The page body is a column of sections, 24px apart; each section carries 24px of internal
  padding.
- A row of label and value sits in a `--surface-alt` field with 16px of padding.

## Composition

A settings screen is: a title, then one section per concern. A section is a heading, one
sentence of explanation, then either a row per value or the single control that acts on it.
Rows never carry more than one control. Destructive actions sit last, in their own section,
behind a confirmation dialog.

## Interaction

- Buttons, checkboxes and dialogs come from `components/ui`; a new screen composes them
  rather than styling its own.
- Every asynchronous action reports its outcome in a toast, and the control it came from
  shows the pending state.
- Loading, empty and error states are part of the screen, not a later addition.
- Keyboard: everything reachable with Tab, dialogs closable with Escape, visible focus ring.

## Copy

Sentence case, short, specific. A hint says what the control does to the system, not how to
use a mouse. No exclamation marks, no marketing adjectives.

## What a screen is not allowed to do

- Invent a second palette, radius scale or type scale.
- Add an icon that is not the control's action.
- Add a navigation entry, a badge or a decorative flourish the product does not already use.
