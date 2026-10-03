import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

export default defineConfig({
  // TODO(domain): SuperDirectory has no domain yet. `.example` is reserved
  // (RFC 2606), so this placeholder can never point at someone else's site.
  // Set the real origin before the first deploy. Canonical URLs, the sitemap,
  // robots.txt and llms.txt all derive from this one line.
  site: 'https://superdirectory.example',

  // Cloudflare Pages serves dist/docs/index.html at /docs/ and redirects /docs
  // to it. 'always' makes every built link and canonical URL the address Pages
  // actually answers on, so no internal link costs a redirect. No URL of this
  // site is indexed yet, so there is nothing older to match.
  trailingSlash: 'always',

  // sitemap-index.xml + sitemap-0.xml, referenced from robots.txt.
  integrations: [sitemap()],

  // Astro 7 changed the default to 'jsx', which deletes whitespace between
  // tags instead of collapsing it to one space. These pages are authored with
  // HTML whitespace semantics, so the pre-v7 behaviour is pinned on purpose,
  // as on Personal-Site. <pre> blocks keep their whitespace either way.
  compressHTML: true,

  build: {
    // The CSP in public/_headers is style-src 'self' with no 'unsafe-inline'.
    // 'never' ships every stylesheet as a file, so no page carries a <style>.
    inlineStylesheets: 'never',
  },

  // One stylesheet for the whole site instead of a chunk per page and per
  // component: every page then makes one CSS request, and the second page a
  // reader opens costs none. The site's CSS is small enough that splitting
  // it saves less than the extra requests cost.
  vite: {
    build: { cssCodeSplit: false },
  },

  // A static content site: no prefetch script, no dev toolbar.
  prefetch: false,
  devToolbar: { enabled: false },
});
