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

**Status.** Done, and promoted (2026-07-08). The Go app is the repository root: two planners
over a shared walk — flatten and organize-by-file-type — both tested, a Charm/Huh wizard, an
interactive recursive exclusion tree (Bubble Tea), and the extractor seam. Cross-compiles to
Windows, Linux, and macOS as single static binaries. The Python script moved to
[`legacy-python/`](./legacy-python/) as the UX reference it was always going to become; the
module is now `github.com/ozzyphantom/SuperDirectory`, so `go install …@latest` works.

## Next steps (build order)

1. Test the organize mode's UX in a real terminal; react to the mode and layout screens.
2. Wire up GoReleaser + Developer ID notarization for real cross-platform releases.
3. Add the remaining content-aware features on the `Extractor` seam: dedup hashing, search.

## Organize by file type (2026-07-08) — done

A second top-level mode, chosen on a new first wizard screen. Where flatten pools every
file into one folder, organize sorts each into `Category/extension/` — the category folds
`.jpg` and `.heic` under `Images/`, the extension keeps them distinguishable inside it.
An optional layout step recreates each file's original folder nesting inside its extension
folder (`Documents/pdf/Work/Invoices/q3.pdf`), so provenance survives the reorganization.

**How it fits the architecture.** `flatten.Item` now carries a destination *relative path*
rather than a bare filename, and `flatten.Copy` creates parent directories on demand
(caching the ones it made). The tree walk moved to `flatten.Walk`, shared by both planners
so exclusion semantics cannot drift between them. A new layout means a new planner, not a
change to the copier. This is the separation the Go rewrite was supposed to buy, and it
held: the new planner is ~100 lines and touches nothing in the copier.

**The classification traps, all tested.** Go's `filepath.Ext(".gitignore")` returns
`".gitignore"`, which would have created a `gitignore/` folder for every dotfile. Extensions
are case-folded so `photo.JPG` and `photo.jpg` share `Images/jpg/`. Compound suffixes stay
whole, so `backup.tar.gz` lands in `Archives/tar.gz/` instead of splitting tarballs by
compressor into `gz/` and `bz2/`.

Trade-off accepted: `jpeg` and `jpg` remain separate extension folders under `Images/`.
Canonicalizing aliases is a one-line change if it grates in practice.

## UX round 2 (2026-07-07) — done

Replaced huh's FilePicker with a purpose-built keyboard directory browser
([`internal/pick`](./internal/pick)) and made the wizard a step machine so every
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
- Content-based classification: a `.pdf` renamed `.txt` is currently sorted as text. The
  `extract.sniff` MIME detector could settle it, at the cost of opening every file.
- A user-editable extension→category table (today it is compiled into `organize.go`).
- Canonicalize extension aliases (`jpeg`→`jpg`, `tif`→`tiff`, `htm`→`html`) so organize
  mode does not split one file type across two folders.
