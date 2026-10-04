---
name: GraphNest web console
description: shadcn/ui new-york, neutral base; a quiet, dense Operate surface.
colors:
  background: "oklch(1 0 0)"
  foreground: "oklch(0.145 0 0)"
  primary: "oklch(0.205 0 0)"
  muted-foreground: "oklch(0.556 0 0)"
  border: "oklch(0.922 0 0)"
  destructive: "oklch(0.577 0.245 27.325)"
  background-dark: "oklch(0.145 0 0)"
  card-dark: "oklch(0.205 0 0)"
  primary-dark: "oklch(0.922 0 0)"
  muted-foreground-dark: "oklch(0.708 0 0)"
  license-permissive: "oklch(0.6 0.11 172)"
  license-weak-copyleft: "oklch(0.76 0.15 78)"
  license-strong-copyleft: "oklch(0.577 0.245 27.325)"
  license-other: "oklch(0.6 0.1 300)"
  license-unknown: "oklch(0.7 0 0)"
typography:
  title:
    fontFamily: "system sans (Tailwind default)"
    fontSize: "1.25rem"
    fontWeight: 600
  body:
    fontFamily: "system sans (Tailwind default)"
    fontSize: "0.875rem"
    fontWeight: 400
  code:
    fontFamily: "system monospace (Tailwind default)"
    fontSize: "13px"
    fontWeight: 400
  metric:
    fontFamily: "system sans (Tailwind default)"
    fontSize: "1.875rem"
    fontWeight: 600
rounded:
  sm: "calc(0.625rem - 4px)"
  md: "calc(0.625rem - 2px)"
  lg: "0.625rem"
  xl: "calc(0.625rem + 4px)"
spacing:
  sm: "8px"
  md: "16px"
---

# Design System: GraphNest web console

## Overview
Operate mode, refinement of the stock shadcn new-york/neutral world: greyscale surfaces, one system sans, lucide icons, 1px borders, almost no shadow. The tool disappears into the task; colour appears only where it carries data. Tokens live in `web/src/index.css`; components are shadcn primitives in `web/src/components/ui`.

## Colors
Neutral greyscale in oklch with a `.dark` override, toggled on `<html>` by the header toggle (light, dark, system). Primary is near-black in light and near-white in dark. `destructive` marks errors, conflicts and strong copyleft. `--chart-1..5` colour series and ecosystems and are theme-specific.

License families have fixed meaning in both themes (`--license-*` in index.css): permissive teal-green, weak copyleft amber, strong copyleft red, other muted violet, unknown grey. Always ordered Permissive, Weak copyleft, Strong copyleft, Other, Unknown. Colour is never the only signal: charts have a table alternative and a legend.

## Typography
One system sans. Page title `text-xl font-semibold`, body `text-sm`, captions `text-xs text-muted-foreground`. Code, paths, SHAs and license expressions use `font-mono`; code viewports (results, file viewer) use 13px. Numeric table columns use tabular numerals (set on `table` in index.css); metric numbers use `tabular-nums`.

## Layout
Collapsible shadcn sidebar plus a 56px header (breadcrumb, theme toggle, sign-out); content padding 16px, `gap-4` between blocks. Search is a 232px rail plus results. Supply-chain views start with one title and a one-line description. KPI strips use a container query: 1, 2, 4, then 7 columns. Tables scroll inside their container; below the `md` breakpoint the sidebar becomes a sheet.

## Elevation & Depth
Flat. Cards use a border; overlays (sheet, dialog, popover) carry the standard shadcn shadow. Opacity (0.3) dims unrelated graph nodes.

## Shapes
`--radius` 0.625rem; inputs and buttons `rounded-md`, cards `rounded-xl`, small chips and legend swatches `rounded-[2px]` to full.

## Components
- Button: variants default, secondary, outline, ghost, link; 3px ring on keyboard focus; disabled at 50% opacity.
- Card/Panel: bordered container with a title; `MetricCard` is label, dominant number, denominator line.
- ChartCard: Recharts through shadcn `chart`, titled and described, with a screen-reader table; charts need an explicit height (`h-72 w-full`, or a row-based height for bars).
- Graph: React Flow, dagre left to right; repositories left, dependencies right; ecosystem colour on the node border and a dot; destructive border for conflict or unlicensed; legend, minimap, zoom, and a "Show as table" switch.
- Sheet: detail panels for components and graph nodes; the title takes focus.
- Forms: label on every control, `ChoiceSelect` for filters.

## Do's and Don'ts
- Do reuse shadcn primitives and tokens; promote repeated values to CSS variables.
- Do name the denominator next to every count.
- Do keep one page title per view; the breadcrumb carries the section.
- Don't put the same sentence in a header and a footer.
- Don't use a coloured side border, gradient text or decorative motion.
- Don't hard-code colours in components; use tokens.
