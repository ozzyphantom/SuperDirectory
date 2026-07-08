# SuperDirectory — Go spike

A working proof-of-concept for the proposed Go rewrite. It exists to answer three questions before committing to a full port:

1. Does the **Charm/huh** wizard feel as good as the current `questionary` + `prompt_toolkit` UX?
2. Does Go deliver the **single static binary on every OS** that the "all operating systems" goal needs?
3. Is the **polyglot seam** (Go core, optional Python extractor later) clean in practice?

All three are demonstrated here.

## Run it

```bash
cd go-spike
go run .                 # interactive flatten wizard (run in a real terminal)
go run . inspect <dir>   # non-interactive: pure-Go content inspection
```

Build a real binary:

```bash
go build -o superdirectory .
./superdirectory
```

## What's here

| Package | Role | Notes |
|---|---|---|
| `internal/flatten` | The functional core: walk + collision-free copy | No TUI knowledge. Pure and unit-tested (`flatten_test.go`). |
| `internal/wizard` | Interactive layer on Charm `huh` | Source → destination → exclusions → confirm. Returns a plain `Result`. |
| `internal/extract` | Content-inspection seam | Pure-Go `MetadataExtractor` today; `PythonExtractor` is the documented future plug-in. |
| `main.go` | Wiring + progress bar + `inspect` demo | lipgloss styling matches the Python app's color language. |

The separation is deliberate: the core has zero dependency on the interactive layer, so it stays fast and testable, and the TUI can be swapped or driven from tests.

## The polyglot seam

`extract.Extractor` is one interface with two backends:

- `MetadataExtractor` — pure Go, zero deps. Sniffs MIME type from content, derives a title from the filename. Ships today.
- `PythonExtractor` — the future. When deep extraction (PDF text, DOCX bodies, OCR) is needed, it shells out to an **optional Python helper process** and decodes JSON. Because the coupling is process-level (not libpython/CGO), the Go binary stays a clean static file and cross-compilation is unaffected. Not wired yet — see the blueprint comment in `extract.go`.

## Verified

```
go test ./...        # core semantics: prefixing, collision suffixes, exclusion, copy
go vet ./...         # clean
```

Cross-compiled from an Apple Silicon Mac, each a single static binary (~7–8 MB):

- `GOOS=windows GOARCH=amd64` → `PE32+ executable`
- `GOOS=linux   GOARCH=amd64` → `ELF, statically linked`
- `GOOS=darwin  GOARCH=arm64` → `Mach-O arm64`

## Intentionally NOT built (spike scope)

- **Recursive per-level exclusion.** The Python app descends level by level; this spike offers a single top-level multiselect (excluding a top-level dir skips its whole subtree — the common case). The recursive wizard maps naturally onto a Bubble Tea model and is the first thing to build next.
- Change-destination / go-back menu in the confirm step.
- Real title extraction from document metadata (the `MetadataExtractor` uses the filename for now).
- Packaging/signing config (GoReleaser + Developer ID notarization).
