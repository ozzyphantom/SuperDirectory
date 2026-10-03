# Handoff — 2026-07-09 — SuperDirectory Go app: run the real photo copy on the new build

## Next step
Run the real 11,041-photo copy with the current build (`git pull && go run .`) and confirm the
stall timeout does its job: the run must finish all 11,041 instead of hanging at 1084/11041,
and the unreadable file must appear in the failures list at the end, named. Watch for a
progress line reading `⚠ no data for 47s  <filename>` — that names the file that used to hang
the whole program.

## Context: what this project is
SuperDirectory copies a nested tree into one "superdirectory" — either flat (every file pooled)
or organized by file type (`Category/extension/`). Oscar uses it to pull scattered technical-doc
scrapes and photo libraries into one folder for upload to things like NotebookLM. It was a
single-file Python script; it's now a Go app at the repo root. Python is preserved in
`legacy-python/` as the UX reference only.

The reason this photo copy matters: Oscar needs it done for a real project. The last three
sessions were driven by him actually using the tool on an external USB 3.1 drive and hitting
real problems, each of which surfaced a genuine bug.

## Where things stand
- All work committed and pushed. `main` @ `11f4c13`, in sync with `origin/main`. Clean tree.
- 8 packages, all tests green, `go test -race` clean on `dedup` + `flatten`, cross-compiles to
  windows/amd64, linux/amd64, darwin/arm64.
- The copy has NOT yet been run on the real 11,041-photo job with the fix. That's the next step.
- The bad file (index 1084, i.e. the 1085th file in lexical order) has NOT been diagnosed. Still
  unknown whether it's damaged or just huge. `lsof`/`sample` diagnostic was offered but the copy
  wasn't hung at the time to run it.

## The drive situation (non-obvious, matters for interpreting results)
- USB 3.1 external drive, has overheating history. Oscar tried AC/cooling; a cool drive still hung
  at the same file count, which is what ruled out thermal (heat isn't deterministic; same-file-count
  twice = one specific bad file).
- Copy throughput measured device-bound at ~85 MB/s on exFAT. Buffer size makes no difference
  (tested 32 KiB → 4 MiB, all within noise). Do NOT add a buffer-size knob — measured dead end.
- macOS exposes no drive temperature. `smartctl -d sat -a /dev/diskN` (needs `brew install
  smartmontools`) works on ~half of USB bridges. `ioreg`/`powermetrics`/`diskutil` give nothing.

## Decisions & why (settled — don't relitigate)
- **Stall timeout is a stall detector, not a per-file deadline.** A 4 GB panorama on a throttled
  drive legitimately takes minutes; the trigger is "no bytes for 60s." `flatten.DefaultStallTimeout
  = 60s`.
- **Abandon, don't cancel.** Go can't cancel a blocked read on a regular file (`SetReadDeadline` is
  pipes/sockets only; nothing unblocks a disk-retry read). Each file copies on its own goroutine
  and is abandoned — descriptors closed, partial dest removed, goroutine left parked in the kernel.
  Deliberate bounded leak. Buffers come from a `sync.Pool` because an abandoned job may still be
  reading into its buffer.
- **Stall detection forces the chunked copy loop**, giving up Linux `copy_file_range` for small
  files. Unavoidable: you can't both hand the copy to the kernel and watch it progress. A test
  (`TestStallTimeoutDoesNotFireOnASlowButMovingFile`) caught the first draft abandoning healthy
  small files because `io.CopyBuffer` reports bytes only at the end.
- **Dedup: content hash, size-gated, within-source, one summary prompt.** Chosen by Oscar. Gate is
  size → 64 KiB partial hash → full hash, so most files are never opened. Survivor of each set is
  earliest in walk order (lexical) — keeps the unsuffixed name (`beach.jpg` not `beach_1.jpg`), but
  means the survivor can be the file *named* like a copy. Oscar was told; open to preferring
  shortest/shallowest path if it grates.
- **Progress bar tracks files, not bytes.** A byte-accurate bar needs an lstat per file up front —
  measured 43× the walk cost on exFAT. Bad trade on the drives where it matters.
- **Rate is smoothed over 300ms, not a lifetime average**, so a drive throttling mid-copy shows a
  falling rate instead of a comfortable average.
- **Directory-listing counts are async** (both picker and exclusion tree), off the critical path.
  Bounding by screen height alone was the wrong axis — the cost was the *size* of each child read,
  not the count. A hop now costs one directory read; subfolder hints arrive from a background worker.

## Files touched (this arc — all committed)
- `internal/flatten/flatten.go`: `Copy` takes `Options{OnProgress, StallTimeout}`; `Progress` has
  `Bytes`/`Current`; `copyJob` type does abandonable per-file copy; `streamCopy` for progress
  through large files; case-insensitive `Unique`; mtime preservation; fsmeta skip in `Walk`.
- `internal/flatten/stall_test.go`: FIFO-based stall tests (build-tagged `!windows`).
- `internal/dedup/dedup.go` + `dedup_test.go` + `stall_test.go`: the whole duplicate finder.
- `internal/fsmeta/`: filesystem-metadata predicate (`._*`, `.DS_Store`, `.Spotlight-V100`, etc.),
  shared by copy + exclusion tree + wizard counts.
- `internal/pick/pick.go`, `internal/exclude/exclude.go`: async subfolder counting.
- `internal/wizard/wizard.go`: mode/layout/duplicates steps; `topLevelSubdirCount`.
- `internal/organize/`: the organize-by-file-type planner.
- `main.go`: `progressDrawer` (rate-limited, stall display), `rateMeter`, `resolveDuplicates`,
  `humanBytes`/`humanRate`/`humanDuration`/`truncateMiddle`.
- `README.md`, `roadmap.md`: kept current with every change.

## Gotchas & open questions
- **TTY-untested.** No screen in the TUI has ever been driven by a human eye — mode, layout,
  duplicates (brand new), async subfolder hints popping in, exclusion tree. All verified by unit
  test only. This is roadmap step 1, still open. If Oscar reports UI weirdness, that's expected
  territory, not a regression.
- **`._` sidecars on exFAT destinations** are cosmetic and NOT our bug — macOS attaches
  `com.apple.provenance` to files written by unsigned binaries. Ad-hoc codesign didn't clear it.
  Retest after Developer ID notarization (roadmap step 2).
- **Dedup untested at scale.** Only run on a 9-file fixture. The size gate *should* keep most of
  11,041 photos unopened, but that's unmeasured on a real tree on slow storage. If Oscar runs the
  copy WITH duplicate detection on, watch how long the hashing stage takes.
- **After the run: diagnose the bad file.** If it's named in the failures, `cp` it by hand — does it
  fail too? If yes, the file is damaged independently of this tool, worth knowing.
- Possible follow-up feature discussed, not built: resume / skip-identical against the *destination*
  (skip files already there with matching size+mtime) — turns an aborted copy into a resume, the one
  thing that'd actually help the heat problem. Enabled by the mtime preservation already shipped.

## Pointers
- Read first: `internal/flatten/flatten.go` (Copy loop ~line 169, copyJob, streamCopy) and
  `roadmap.md` (every decision has a dated section).
- Resume the job: `git pull && go run .` — needs a real terminal.
- Re-verify: `go test ./...`, `go test -race ./internal/dedup/ ./internal/flatten/`,
  `gofmt -l .`, `go vet ./...`.
- Repo: `github.com/ozzyphantom/SuperDirectory` (public). Latest commits `c8bc8b8` (stall timeout),
  `11f4c13` (dedup).
- Scratchpad for test fixtures (hdiutil exFAT/FAT32 images, fake photo trees):
  `/private/tmp/claude-501/-Users-oscargarcia-projects-SuperDirectory/fae59801-de9a-485d-b7df-38b2574ff9f0/scratchpad`
