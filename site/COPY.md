# Copy placeholders

Every place on this site where a person writes the text. An agent wrote none of it: under the UX standard (§2, directive 02.1), an agent writes no non-technical copy. Each place shows a dashed box on the page that reads `[COPY · id · length: kind]`.

The site's agent-written text is limited to technical definitions, procedures, labels, and status messages. `/disclosures/` says so on the site.

## Fill a place

1. Write the text.
2. Open the source file in the table.
3. Replace the placeholder with your text:
   - For a `<Placeholder id="…" />` element, put the real element in its place, for example `<h1 class="hero__title">Your heading</h1>`.
   - For a `[COPY · …]` string, replace the whole string.
4. Set the row's status to `filled`.
5. Run `corepack pnpm run verify` in `site/`. The copy check fails when this table and the built site disagree.

## Placeholders

| id | page | location | source file | kind of copy | suggested length | status |
|----|------|----------|-------------|--------------|------------------|--------|
| `home-hero-heading` | `/` | Hero: the page's `<h1>` | `src/pages/index.astro` | Headline: what SuperDirectory does | 6–10 words | open |
| `home-hero-subline` | `/` | Hero: the line under the heading | `src/pages/index.astro` | Sub-line: who it is for, and the problem it solves | 1 sentence, 15–25 words | open |
| `home-features-intro` | `/` | Features: under the section heading, above the six groups | `src/pages/index.astro` | Section intro | 1–2 sentences | open |
| `home-meta-description` | `/` | `<meta name="description">`: search results and link previews, not shown on the page. Also the `description` of `/index.md` | `src/pages/index.astro`, the `description` of `<Base>` | Page summary | 120–160 characters | open |
| `llms-summary` | `/llms.txt` | The `>` summary line under the title | `src/pages/llms.txt.ts` | What SuperDirectory is, for agents that read the site | 1–2 sentences | open |

## Other placeholders (not copy)

| what | where | note |
|------|-------|------|
| Wordmark | `src/components/Wordmark.astro` | Inline SVG placeholder mark beside the product name in the header. Keep the real one inline and in `currentColor`. |
| Icon | `src/layouts/Base.astro` | No icon file yet. An empty `data:` icon stops requests for `/favicon.ico`. Add an icon with the real wordmark. |
| Domain | `astro.config.mjs`, `site:` | `https://superdirectory.example` with a `TODO(domain)` comment. Canonical URLs, the sitemap, `robots.txt`, `llms.txt` and the Markdown editions all follow it. |
