## Design Context

### Users
Developers across the full spectrum — solo engineers building side projects, startup teams integrating sandboxed execution into products, and platform/infra engineers at larger organizations running production workloads on Cloud Hypervisor microVMs. They arrive with context: they know what a process is, what a rootfs is, what a TTY means. The interface must feel at home for all three: approachable enough not to intimidate a hacker, precise enough to earn the trust of a production ops team. Never condescend, never oversimplify. Trust the user to understand what they're looking at.

**Primary job to be done:** Understand what's running, act on it confidently, and get back to code.

### Brand Personality
**Precise. Warm. Uncompromising.**

Wrenn is an engineer's favorite tool — built with visible care, not assembled from defaults. It runs real infrastructure (Cloud Hypervisor microVMs), so the UI should reflect that seriousness without becoming cold or corporate. The warmth comes from the typography and color palette; the precision comes from hierarchy, density, and data fidelity.

Emotional goal: **in control.** Users leave a session with full confidence in what's running, what happened, and what comes next. Nothing is hidden, nothing is ambiguous.

### Aesthetic Direction
**Dark-only (permanently), industrial-warm, data-forward.**

No light mode planned. All design decisions should optimize for dark. The near-black-green background palette (`#0a0c0b` through `#2a302d`) reads as "black with intention" — not pitch black (cold) and not charcoal (dated). The sage green accent (`#5e8c58`) is muted and organic, a meaningful departure from the startup-green neon that saturates the developer tool space.

**Anti-references:**
- **Supabase**: avoid the friendly, approachable startup-green energy — too generic, too eager to please
- **AWS / GCP consoles**: avoid utility-first density without craft — functional but joyless, visually dated

**References that capture the right spirit:**
- The precision of a well-calibrated instrument
- Editorial typography from technical publications
- The quiet confidence of tools that don't need to explain themselves

### Type System
Four fonts with strict roles — this is the design system's strongest personality trait and must be respected:

| Font | CSS Class | Role | When to use |
|------|-----------|------|-------------|
| **Manrope** (variable, sans) | `font-sans` | UI workhorse | All body copy, nav, labels, buttons, form text |
| **Instrument Serif** | `font-serif` | Display / editorial | Page titles (h1), dialog headings, metric values, hero moments |
| **JetBrains Mono** (variable) | `font-mono` | Data / code | IDs, timestamps, key prefixes, file paths, terminal output, metrics |
| **Alice** | brand wordmark only | Brand wordmark | "Wrenn" in sidebar and login only — nowhere else |

Instrument Serif at scale creates the signature editorial moments. Mono provides the precision signal for technical data. Never swap these roles.

**Tracking overrides (app.css):**
- `.font-serif` — `letter-spacing: 0.015em` (positive tracking; Instrument Serif reads less condensed at display sizes)
- `.font-mono` — `font-variant-numeric: tabular-nums` (numbers align in tables and metric displays)

**Type scale (root: 87.5% = 14px base):**
| Token | Value | Use |
|---|---|---|
| `--text-display` | 2.571rem (~36px) | Auth section headings |
| `--text-page` | 2rem (~28px) | Page h1 titles |
| `--text-heading` | 1.429rem (~20px) | Dialog headings, empty states |
| `--text-body` | 1rem (~14px) | Primary body, buttons, inputs |
| `--text-ui` | 0.929rem (~13px) | Nav labels, table cells |
| `--text-meta` | 0.857rem (~12px) | Key prefixes, minor info |
| `--text-label` | 0.786rem (~11px) | Uppercase section labels |
| `--text-badge` | 0.714rem (~10px) | Live badges, tiny indicators |

### Color System

All values are CSS custom properties in `frontend/src/app.css`.

**Backgrounds (6-step near-black-green scale):**
| Token | Value | Use |
|---|---|---|
| `--color-bg-0` | `#0a0c0b` | Page base, sidebar deepest layer |
| `--color-bg-1` | `#0f1211` | Sidebar surface |
| `--color-bg-2` | `#141817` | Card backgrounds |
| `--color-bg-3` | `#1a1e1c` | Table headers, elevated surfaces |
| `--color-bg-4` | `#212624` | Hover states, inputs |
| `--color-bg-5` | `#2a302d` | Highlighted items, selected rows |

**Text (5-level hierarchy):**
| Token | Value | Use |
|---|---|---|
| `--color-text-bright` | `#eae7e2` | H1s, dialog headings |
| `--color-text-primary` | `#d0cdc6` | Body copy, primary labels |
| `--color-text-secondary` | `#9b9790` | Secondary labels, descriptions |
| `--color-text-tertiary` | `#6b6862` | Hints, placeholders |
| `--color-text-muted` | `#454340` | Dividers as text, ultra-subtle |

**Accent (sage green — use sparingly, must feel earned):**
| Token | Value | Use |
|---|---|---|
| `--color-accent` | `#5e8c58` | Primary CTA, live indicators, focus rings, active nav |
| `--color-accent-mid` | `#89a785` | Hover accent text |
| `--color-accent-bright` | `#a4c89f` | Accent on dark backgrounds |
| `--color-accent-glow` | `rgba(94,140,88,0.07)` | Subtle tinted backgrounds |
| `--color-accent-glow-mid` | `rgba(94,140,88,0.14)` | Hover tint on accent items |

**Status semantics:**
| Token | Value | Use |
|---|---|---|
| `--color-amber` | `#d4a73c` | Warning, paused state |
| `--color-red` | `#cf8172` | Error, destructive actions |
| `--color-blue` | `#5a9fd4` | Info, neutral system states |

**Borders:** `--color-border` (`#1f2321`) default; `--color-border-mid` (`#2a2f2c`) for inputs/hover.

### Component Patterns

**Buttons:**
- Primary: solid sage green (`--color-accent`), hover brightness boost + micro-lift (`-translate-y-px`)
- Secondary: bordered (`--color-border-mid`), text transitions to accent on hover
- Danger: red text + subtle red background on hover
- All: `transition-all duration-150`

**Inputs:**
- Border `--color-border`, background `--color-bg-2`; focus transitions border and icon to accent
- Group focus pattern: `group` wrapper + `group-focus-within:text-[var(--color-accent)]` on icon

**Tables / data lists:**
- Grid layout; header `bg-3` + uppercase `--text-label`; row hover `hover:bg-[var(--color-bg-3)]`
- Status stripe: left border color matches sandbox state

**Status indicators:** Running = animated ping + sage green dot; Paused = amber dot; Stopped = muted gray. Color is never the sole differentiator.

**Modals & dialogs:** Border + shadow only — no accent gradient bars/strips. `fadeUp` 0.35s entrance.

**Empty states:** Large icon with glow, Instrument Serif heading, secondary body text, CTA below, `iconFloat` 4s animation.

**Animations (always respect `prefers-reduced-motion`):** `fadeUp` (entrance), `status-ping` (live indicator), `iconFloat` (empty states), `spin-once` (refresh), staggered `animation-delay` on lists.

### Design Principles

1. **Precision over friendliness.** Every element earns its place. Wrenn doesn't need to tell you it's developer-friendly — that should be self-evident from the quality of the information architecture.

2. **Density with breathing room.** Data-forward doesn't mean cramped. Strategic whitespace creates calm hierarchy within dense contexts. Sections breathe; rows don't waste space.

3. **Industrial warmth.** The serif + mono + warm-black combination prevents sterility. This is a forge, not a gallery. The warmth is in the details, not the primary colors.

4. **Legible at speed.** Users scan dashboards in seconds. Strong typographic contrast (serif h1, mono IDs, sans body), consistent patterns, and predictable placement let users orientate instantly without reading everything.

5. **Craft signals trust.** For infrastructure that runs production code, the quality of the UI is a proxy for the quality of the product. Pixel-level decisions matter. Polish is not decoration — it's a trust signal.
