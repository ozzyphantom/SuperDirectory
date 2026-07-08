# SuperDirectory — Roadmap

## Direction: rewrite in Go (decided 2026-07-07)

**Decision:** Rewrite SuperDirectory in Go. The current Python app is the reference
for behavior and UX, not the long-term codebase.

**Why the speed question was the wrong question.** The core job (walk a tree, copy
files) is I/O and syscall bound. Python's `shutil.copy` already calls the same kernel
primitives (`fcopyfile`, `copy_file_range`) a compiled language would. No rewrite makes
today's copy faster. Language choice only pays off at two gates, and both are about the
roadmap, not the current feature:

1. **Real parallelism without a GIL** — for future CPU work: dedup hashing, content-type
   sorting, title scraping, search over big trees.
2. **A zero-runtime single binary** — for "runs on all operating systems." Today's
   `start.sh` is bash, so it is dead on native Windows, and it assumes system Python plus
   network pip. Both Python paths fail both gates.

**Why Go over Rust and Python.** Among the compiled options, throughput on this I/O-bound
workload is a near-tie, so the tiebreakers decide it:

- **Agent-codeability with few iterations.** This app is built by directing an AI agent.
  Go gives a sub-second compiler that catches nil/type/signature errors on every edit,
  plus a large clean training corpus, so first-draft code is usually correct. Rust catches
  more at compile time but needs more fix cycles, and its borrow checker fights this app's
  messiest code (the shared-mutable exclusion state machine).
- **Distribution is a tie with Rust, and a win over Python.** `CGO_ENABLED=0 go build`
  cross-compiles every OS from one Mac, no toolchain. GoReleaser publishes to Homebrew,
  Scoop, Winget, and more.
- **The TUI gets better.** The Charm stack (Bubble Tea, Huh, Lip Gloss, Bubbles) is the
  strongest interactive TUI in any language right now.

Rust stays the runner-up: pick it only if the app ever becomes genuinely compute-bound.

**Polyglot seam for deep extraction.** Document parsing (full PDF text, DOCX bodies, OCR)
is Go's weakest domain and Python's strongest. The plan keeps a single `Extractor`
interface: a pure-Go metadata extractor ships by default, and if deep extraction is ever
needed, an optional Python helper plugs in behind the same interface by running as a
**subprocess** (not libpython/CGO), so the Go binary stays a clean static file. Titles
specifically live in file metadata and are readable in pure Go, so the near-term roadmap
item does not force Python.

**Distribution note.** Apple Developer license is in hand, so macOS notarization is
settled. Single-binary install is frictionless on macOS, Windows, and Linux.

**Status.** Working proof-of-concept at [`go-spike/`](./go-spike/): the flatten core
(tested), a Charm/Huh wizard, an interactive recursive exclusion tree (Bubble Tea), and
the extractor seam. Cross-compiles to Windows, Linux, and macOS as single static binaries.

## Next steps (build order)

1. Test the spike's UX in a real terminal; react to the picker and exclusion-tree feel.
2. Wire up GoReleaser + Developer ID notarization for real cross-platform releases.
3. Add content-aware features on the `Extractor` seam: type sorting, dedup hashing, search.

## UX round 2 (2026-07-07) — done

Replaced huh's FilePicker with a purpose-built keyboard directory browser
([`internal/pick`](./go-spike/internal/pick)) and made the wizard a step machine so every
screen can go back. Fixes from Oscar's test: typing now jumps the cursor instead of opening
a text box; the source opens at home, not root; a cyan theme replaces huh's hard-to-read
purple. An adversarial review (3 dimensions, findings independently refuted) caught and fixed
seven more: `q` aborting the whole app, a case-insensitive hole in the copy-into-itself guard
(a real data-loss risk on APFS), stepping back wiping prior exclusion picks, unreadable
folders looking empty, and preview overflow hiding the help line.

Known trade-offs left in place: the case-insensitive overlap guard can over-block two
siblings that differ only in case on a case-sensitive volume (safe direction); and the
exclusion tree does not yet restore *scroll/expansion* state on re-entry, only the chosen
set.

## Feature ideas

- Option to scrape all titles from the documents listed.
