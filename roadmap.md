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

## External drives (2026-07-08) — verified, one bug fixed

Tested against real exFAT and FAT32 volumes (`hdiutil` disk images), in every direction:
internal→drive, drive→internal, drive→drive. Both modes. Cross-device copy works, nested
directory creation works, and filenames that are legal on APFS but reserved on exFAT
(`?`, `*`, `|`) are remapped by the macOS driver rather than failing.

**Silent data loss, found and fixed.** `flatten.Unique` reserved destination names
case-sensitively. Pooling by file type discards the directory, so `Trip/beach.JPG` and
`Work/Beach.jpg` were planned as two distinct destinations — and on any case-insensitive
volume (APFS, NTFS, exFAT, FAT32, i.e. almost everywhere) the second copy silently
overwrote the first. Two files in, one file out, zero failures reported. Reservation is now
case-insensitive; the returned filename keeps its original case. On a truly case-sensitive
volume this only ever adds a `_1` to two names differing solely in case — the safe
direction, matching the copy-into-itself guard's existing trade-off.

Not external-drive specific: the same loss happened copying to the internal disk. The
question surfaced it.

**Filesystem metadata, skipped.** Copying *from* a Mac-formatted drive dragged along macOS
bookkeeping: AppleDouble `._` sidecars plus `.fseventsd/`, `.Spotlight-V100/`, `.DS_Store`.
Flattening a drive root produced 86 files, 51 of them junk. The new
[`internal/fsmeta`](./internal/fsmeta) holds one predicate, shared by `flatten.Walk`, the
exclusion tree, and the wizard's file counts — so the copy, the tree you pick from, and the
"3 files · 2 dirs" labels all agree about what exists. Had only the copy filtered, the tree
would have offered to exclude directories that were never going to be copied. The same drive
root now flattens to 3 files, 0 junk. Dotfiles a user wrote (`.gitignore`, `.env`, `.git/`)
are content, not metadata, and are still copied; and choosing `.Trashes` as your source
deliberately still copies what is inside it.

**Modification times, preserved.** `flatten.Copy` now calls `os.Chtimes` after each file, so
an archival copy no longer lands claiming every file was written today. A failure to set the
timestamp is deliberately not fatal — the contents are already safely on disk, and failing
the file would send the user hunting for data that arrived intact. Verified across a
filesystem boundary in both directions, including onto FAT32 and its two-second granularity.

**Not fixed, cosmetic.** Writing to exFAT produces hidden `._` sidecars on the destination.
Traced to macOS attaching a `com.apple.provenance` xattr to files created by unsigned
binaries — a bare `os.WriteFile` triggers it, so it is not the copy logic, and ad-hoc
code-signing does not clear it. Invisible in Finder. Possibly resolved by Developer ID
notarization; retest at that point.

## Navigation performance (2026-07-08) — fixed

Browsing an external drive was slow. The cause was I/O amplification in the TUI, not the
copy: three places read every child directory to render a label.

`pick.load()` counted each child's subfolders to print "(3 subfolders)". Entering a folder
with 120 children issued **121 directory reads** to draw 15 rows. `exclude.loadChildren` did
the same on every expand. `wizard.topLevelSubdirs` built a `[]subdir` carrying per-directory
file and folder counts **that no caller ever read** — 121 reads to compute one integer for a
description string. And the exclusion preview read its directory **twice per keystroke**,
because `previewHeight` and `renderPreview` each called `os.ReadDir` independently.

Locally these cost microseconds. On a USB drive each read is a bus round trip, and a seek on
a spinning disk.

**Round one — bound the number of child reads.** Counts were deferred until a row was about
to be drawn, and memoized. Work became proportional to terminal height, not directory size:
`pick.load()` went from 121 reads / 79 ms to 16 reads / 16 ms. The wizard's dead helper was
deleted outright, along with `subdir`, `topLevelSubdirs`, and `topLevelHasSubdirs`. The
preview listing was cached on its node.

**Round two — take the child reads off the critical path.** Round one was only slightly
faster on a real drive, and it still slowed down with every hop deeper. The remaining cost
was the *size* of each child read, not the count of them. Counting one child means reading
that child's whole directory: a folder holding 800 files and one subfolder costs 801 entries
to learn the number "1". Fifteen of those before the first frame, and documentation scrapes
hold more files the deeper you go — hence the depth correlation. Bounding by screen height
was the wrong axis.

Counting is now asynchronous in both the picker and the exclusion tree. A hop costs exactly
one directory read — the one you entered — and subfolder hints arrive a moment later, from a
background worker whose results land through the normal Bubble Tea message loop. The picker
carries a generation counter, so hopping quickly does not leave a queue of workers competing
for the disk; a stale worker stops between children and its result is discarded. In the tree,
nodes are stable objects, so results are always safe to apply, and a `counting` flag keeps a
node from being dispatched twice.

Two things fell out. `expandCurrent` used to establish `hasSubdirs` (one directory read) and
then call `loadChildren` on the very same directory (a second read); it now just calls
`loadChildren` and expands if there is anything inside. And an unknown count must render as
*no label*, never `(0 files, 0 dirs)` — a zero is a lie about a folder, not a placeholder.

Measured cold on exFAT, 15 subfolders each holding 800 files: a hop went from **45 ms to
1.8 ms**, with the 46 ms of child reads moved off the path you wait on. Tests assert that
`load()` and `loadChildren` touch no child directory at all, that stale results are discarded,
and that in-flight nodes are never dispatched twice. `go test -race` is clean.

A side effect worth noting: `previewHeight` used to return 2 lines on a read error while
`renderPreview` drew 0, silently misaligning the layout. Sharing one memoized listing removed
the disagreement, and a test now pins reserved height to rendered height.

## Copy throughput (2026-07-08) — measured, not optimized

Copying to an external drive felt slow. Two hypotheses, both tested, both wrong; the third
finding is a display gap, not a performance one.

**Is the copy syscall-bound?** On macOS, `os.File.readFrom` is a stub — `zero_copy_stub.go`
is built for every GOOS except freebsd, linux, and solaris — so `io.Copy` between two files
falls back to a 32 KiB buffered loop. A 30 MB photo becomes ~960 read/write pairs. Tempting.
Measured against exFAT, `fsync`'d: 32 KiB → 85.7 MB/s, 64 KiB → 88.8, 256 KiB → 84.2,
1 MiB → 81.2, 4 MiB → 77.9. Cutting syscalls 137-fold changes nothing, and bigger buffers are
marginally *worse*. The copy is bound by the device. **The buffer stays at the standard
library default**; a knob with a story and no evidence is worse than no knob. (A stale comment
claiming `fcopyfile on macOS` was removed — no such fast path exists in Go.)

**Would a byte-accurate progress bar be free?** No. Learning each file's size means
`d.Info()`, which on exFAT costs an `lstat` per file. Walking 2000 files: 4 ms without sizes,
175 ms with — **43x slower**, on an SSD-backed image, before the copy even starts. On the USB
drives where progress matters most, that is a visible stall. The bar therefore tracks files,
not bytes.

**What was actually missing was the number.** `flatten.Copy` now reports a `Progress`
(files done, bytes written, elapsed) after each file, and the progress line shows bytes copied,
a smoothed throughput in MB/s, and an estimated time remaining. Bytes count only successful
writes, so a failing copy cannot look fast. The rate is exponentially smoothed over a 300 ms
resample interval rather than averaged over the run: a lifetime average hides the moment a
drive begins to throttle, which is exactly what you want to see. The ETA extrapolates from
files completed — honest for a folder of photos, poor for a mixed tree — and is suppressed
below one second and until three files have landed.

Not done, deliberately: parallel copies. Concurrency can raise throughput on flash and lower
it on a spinning disk, and nothing here has been measured against real hardware.

**Round two — the bar could not tell a big file from a hang.** Progress was reported only
after each file returned. Through one large file the bar, the byte count, and the rate all
froze at their last values, naming nothing. A copy stopped dead at "9%, 1084/11041" gave no
way to tell a 4 GB panorama from an unreadable sector, and sent the user to `lsof`.

`Copy` now reports *before* opening each file, so the name of a file that then blocks is
already on screen; periodically while a file over 8 MiB streams, so a big file visibly moves;
and once on completion. Below that threshold `io.CopyBuffer` is used instead, which lets
Linux's `copy_file_range` engage — a chunked loop would disable it, and small files finish
faster than a frame anyway. The display rate-limits itself to ~16 fps, because `Copy` now
reports thousands of times a second on a folder of small files.

The meter tracks stalls separately from throughput: the rate is resampled every 300 ms, but a
stall is measured from the last byte that actually *moved*. After five still seconds the line
reads `⚠ no data for 47s` beside the filename, and drops the ETA, which would be a fiction.
That turns "the app hung" into "this one file will not read".

## Stall timeout (2026-07-08) — done

A copy stopped dead at 1084/11041 files, twice, at the same file, with the drive cool. Not
thermal — heat is not deterministic. One file would not read.

**The trigger is a stall, not a deadline.** A 4 GB panorama on a throttled drive legitimately
takes minutes; a file delivering no bytes for a minute is stuck. `Options.StallTimeout`
(default 60s) abandons it, records a `StallError` in the failures, and moves on.

**Go cannot cancel a blocked read on a regular file.** `SetReadDeadline` works only on
pollable descriptors — pipes and sockets — and no syscall unblocks a read parked in a disk
retry. So each file copies on its own goroutine and is *abandoned*: descriptors closed,
partial destination removed, `Copy` moves on. The goroutine stays parked in the kernel until
the read finally returns. That is a deliberate leak, bounded by the number of unreadable
files, and it beats hanging the whole program on one bad sector. A `sync.Pool` supplies copy
buffers precisely because an abandoned job may still be reading into its buffer long after
`Copy` has moved to the next file.

**A test found a real bug in the first draft.** Files under 8 MiB used `io.CopyBuffer`, which
reports its bytes once, at the end — so the counter sat at zero for the file's whole life and
the stall detector abandoned a slow-but-healthy file. On a sick drive a 7 MB file can take
over a minute. Enabling stall detection now forces every file through the chunked loop. The
cost is Linux's `copy_file_range` for small files: you cannot both hand the copy to the kernel
and watch it progress. That trade is recorded in `Options.StallTimeout`.

Partial destinations are now removed on any failure. A silently truncated photo sitting in the
output is worse than a missing one that is named in the failures.

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
- A user-visible toggle for the metadata skip list, for the rare source where `._*` files are
  the content (a forensic image, an AppleDouble archive).
