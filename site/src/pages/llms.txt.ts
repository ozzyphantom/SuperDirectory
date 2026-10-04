import type { APIRoute } from 'astro';

/* llms.txt (llmstxt.org): the map of this site for agents. Built from `site`
   so every URL follows the real domain when it lands.

   The summary line under the title is copy, so it is a placeholder for a
   person to write (UX standard [02.1]); it is listed in COPY.md. The page
   lines below it are technical descriptions of what each page holds. */
export const GET: APIRoute = ({ site }) => {
  const at = (path: string) => new URL(path, site).href;
  const body = [
    '# SuperDirectory',
    '',
    '> [COPY · llms-summary · 1–2 sentences: what SuperDirectory is, for agents]',
    '',
    '## Pages',
    '',
    `- [Home](${at('/')}): install methods (Homebrew, download, Go), the duplicates step as text, and a one-line definition of each feature`,
    `- [Docs](${at('/docs/')}): wizard steps, keys, commands and copy flags, presets, duplicate rules, NotebookLM limits, resume, and the copy report`,
    `- [Disclosures](${at('/disclosures/')}): which text on this site an AI wrote, and which text a person writes`,
    '',
    '## Markdown editions',
    '',
    'Every page is also served as Markdown: send `Accept: text/markdown`, or fetch',
    `\`<page>/index.md\` directly (for example ${at('/docs/index.md')}).`,
    '',
    '## Elsewhere',
    '',
    '- [Source](https://github.com/ozzyphantom/SuperDirectory): the program’s repository and README',
    '- [Releases](https://github.com/ozzyphantom/SuperDirectory/releases/latest): archives to download',
    '',
  ].join('\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
