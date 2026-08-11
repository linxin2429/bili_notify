# Bili Notify Administration UI

## Visual Theme & Atmosphere

Bili Notify is a compact operations ledger: calm, direct, and optimized for repeated status checks. The interface uses a paper-like flat canvas, near-black typography, strict rules, and an asymmetric editorial grid. Its memorable gesture is the oversized operational verdict on the overview page. Visual variance is 4/10, motion is 3/10, and information density is 7/10.

## Color Palette & Roles

- Canvas: `#f4f3ef` in light mode and `#111210` in dark mode.
- Primary surface: `#fbfaf6` and `#191a17`; raised surface is reserved for overlays.
- Ink: `#171816` and `#f2f1eb`; muted ink: `#676963` and `#a8aaa3`.
- Hairline: `#d2d1ca` and `#363833`.
- Interaction/status cyan: `#006d7e` (light), `#63d5e6` (dark). Use for links, focus, active navigation, and live status. The light value is darkened from the concept cyan to meet WCAG AA on paper surfaces.
- Attention vermilion: `#c43b22` (light), `#ff8068` (dark). Use for warnings, destructive actions, queue badges, and failed checks.
- Success: `#287044` (light), `#74cf91` (dark). Use only for confirmed success semantics.
- Do not use gradients, glows, or decorative color washes.

## Typography Rules

Prefer the self-hosted `Noto Sans SC Variable` webfont for deterministic Chinese metrics, then fall back to the system stack: `"Noto Sans SC Variable", -apple-system, BlinkMacSystemFont, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans SC", sans-serif`. Page titles are 28-34px at weight 760; compact section titles are 16-18px at weight 700; body copy is 14-15px; labels and metadata are 12-13px. Chinese text always uses zero letter spacing. Operational numbers use `font-variant-numeric: tabular-nums`; identifiers may use the system monospace stack.

## Component Stylings

- Buttons: 4px radius, 40px minimum height, clear border or filled command treatment. Icon-only tools use Lucide icons and a tooltip/accessible label.
- Cards: 4px radius and a 1px hairline. Use only for repeated entities, dialogs, or genuinely framed tools; page sections remain open and rule-separated.
- Inputs: 4px radius, 40-44px height, flat surface, cyan focus border and ring.
- Navigation: integrated full-height desktop sidebar; active items use a subtle flat cyan tint without an accent stripe. Mobile uses five fixed destinations: 概览、历史、队列、采集源、更多.
- Badges: compact, low-radius labels; reserve pills for short statuses or counts.
- Alerts: full border and semantic background, never a left accent stripe.
- History comments: full hairline border and flat background; nesting is shown by spacing, not a left-edge accent line.

## Layout Principles

Use an 8px base grid with 4px for compact internal alignment. Desktop content is capped at 1440px and uses dense 12-column or asymmetric two-column layouts. Overview metrics form a horizontal ledger separated by rules, never four equal cards. Section boundaries rely on whitespace and hairlines. Keep touch targets at least 40px and avoid nesting cards.

## Depth & Elevation

Follow IBM Carbon's flat background layering: canvas and surface changes provide hierarchy. Following HashiCorp's restrained enterprise elevation, ordinary content has no shadow; only dialogs, menus, and toasts use `0 16px 44px rgb(0 0 0 / 18%)`. Corners stay within 4-8px except circular avatars and switches.

## Do's and Don'ts

Do prioritize current health, blocked work, and next actions. Do use real operational data and concise Chinese labels. Do preserve accessible focus, loading, empty, error, and destructive confirmation states. Do not add marketing copy, decorative numbering, `OPERATIONS` kickers, oversized card grids, purple gradients, blur-heavy chrome, italics, floating sections, nested cards, or Unicode action/status glyphs where a Lucide icon exists.

## Responsive Behavior

At 900px, hide the desktop sidebar and show a fixed five-item bottom navigation. Reflow metric ledgers to two columns, then one column below 600px. Collapse overview columns and filter grids without changing information priority. On History, keep keyword search visible and place secondary filters behind a progressive disclosure control. Prevent labels, badges, and actions from clipping at 390x844; dialogs become near-full-width and full-screen only when explicitly requested.

## Agent Prompt Guide

Build a dense Swiss-style operations ledger for Bili Notify. Use a paper canvas, near-black ink, cyan interaction/status, vermilion attention, 8px spacing, 4px corners, flat surfaces, hairline separators, tabular numerals, Lucide icons, an integrated sidebar, and one dominant health verdict. Keep motion under 300ms and only for button press, dialog, toast, or infrequent state transitions. Honor `prefers-reduced-motion`. Preserve light and dark themes. The concept preview's numbered navigation and English kicker are intentional omissions because they are decorative rather than operational.
