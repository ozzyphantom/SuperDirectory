# SuperDirectory

An interactive CLI tool that rebuilds a nested directory tree as a **superdirectory** — either one flat folder holding every file, or a folder per file type. Your source directory is never modified; everything is copied.

A single static binary. No runtime, no dependencies, no `pip install`.

## What It Does

Given a source directory like this:

```
my-project/
├── src/
│   ├── main.py
│   └── utils.py
├── tests/
│   └── test_main.py
├── notes.pdf
└── README.md
```

**Flatten** pools every file into one folder. Files in the source root keep their names; files from subdirectories are prefixed `parentdir_` to preserve context. If a collision still occurs, a numeric suffix (`_1`, `_2`, …) is appended.

```
my-project-super/
├── README.md
├── notes.pdf
├── src_main.py
├── src_utils.py
└── tests_test_main.py
```

**Organize by file type** sorts every file into `Category/extension/`. The category folds related formats together (`.jpg` and `.heic` both land under `Images/`); the extension keeps them distinguishable inside it.

```
my-project-super/
├── Code/
│   └── py/
│       ├── main.py
│       ├── test_main.py
│       └── utils.py
├── Documents/
│   ├── md/README.md
│   └── pdf/notes.pdf
```

Organize mode can optionally **keep the original folders** inside each extension folder, so you can still see where a file came from:

```
my-project-super/
└── Code/
    └── py/
        ├── src/main.py
        └── tests/test_main.py
```

## Why

1. I created this utility to help me skim through scrapes of proprietary/obscure technical documentation. I use these scrapes to assist in my work troubleshooting and solution engineering communication between aging hardware with modern networking infrastructure. These databases are often nested in tens or hundreds of subdirectories, many of which are unrelated to products I work with or are aged out of relevancy. This utility allows me to sort through directories with ease while keeping a backup of the entire documentation system structure if needed. Copying these files into a SuperDirectory allows for easy multi-file uploads to services such as NotebookLM to ask questions of the database without including irrelevant documents.
2. This project was built partially as a UX design showcase using Claude Code. The included [UX Adjustment History](UX-Adjustment-History.md) document showcases follow-up prompts during testing of the application to make it as user-friendly and fun to use as possible.

It began as a single-file Python script. It was [rewritten in Go](roadmap.md) to become a zero-runtime single binary that works on every OS, and to leave room for the CPU-bound features on the roadmap. The original script is preserved in [`legacy-python/`](legacy-python/) as the UX reference.

## Features

- **Two modes** — flatten into one folder, or organize into a folder per file type
- **Interactive wizard** — guided step-by-step prompts, and every screen can go back
- **Directory browser** — a keyboard-driven picker for source and destination, with type-to-jump
- **Exclusion system** — selectively skip subdirectories at any depth, with a recursive tree
- **Directory preview** — inspect directory contents before deciding to include or exclude
- **Safe by default** — refuses a destination inside the source, so a copy can never eat itself

## Requirements

Go 1.25 or newer — **only to build**. The resulting binary has no runtime dependencies.

## Installation

Install the binary straight from the repo:

```bash
go install github.com/ozzyphantom/SuperDirectory@latest
superdirectory
```

Or clone and build:

```bash
git clone https://github.com/ozzyphantom/SuperDirectory.git
cd SuperDirectory
go build -o superdirectory .
./superdirectory
```

## Usage

Run it with no arguments to start the wizard. It must run in a real terminal.

```bash
go run .                 # from a clone, without building
./superdirectory         # a built binary
```

There is one other command, useful for a quick look at what the content extractor sees:

```bash
go run . inspect <dir>   # non-interactive: pure-Go content inspection
```

The wizard walks you through six steps. Two of them are skipped when they don't apply.

1. **Mode** — flatten into one folder, or organize by file type
2. **Source** — browse to the directory to copy from
3. **Destination** — browse to where the superdirectory goes, and name it
4. **Exclusion** — optionally skip subdirectories (skipped when the source has none)
5. **Layout** — keep the original folders inside each type folder? (organize mode only)
6. **Confirmation** — review, go back, or copy

### Keyboard Controls

| Context | Key | Action |
|---|---|---|
| Everywhere | `Ctrl+C` | Exit the program |
| Everywhere | `Esc` | Go back a step |
| All menus | `↑` / `↓` | Navigate choices |
| All menus | `Enter` | Confirm selection |
| Directory browser | `→` | Open the highlighted directory |
| Directory browser | `←` / `Backspace` | Go up to the parent directory |
| Directory browser | any letter | Jump to the next entry starting with it |
| Directory browser | `Enter` | Choose the current folder |
| Naming a folder | type, then `Enter` | Name and create the destination |
| Exclusion tree | `Space` | Exclude / re-include the highlighted directory |
| Exclusion tree | `→` / `←` | Expand / collapse a directory |
| Exclusion tree | `p` | Preview highlighted directory |
| Exclusion tree | `Enter` | Done — accept the exclusions |
| Exclusion tree | `q` | Quit the program |

Navigation in the directory browser is on the arrow keys so every letter stays free for type-to-jump. The exclusion tree also accepts `j` / `k` / `h` / `l` as vim-style movement.

## How files are classified

Organize mode sorts each file into `Category/extension/`. Known categories: Archives, Audio, Code, Documents, Fonts, Images, Presentations, Spreadsheets, Video, and `Other/` for everything else.

Three details worth knowing:

- **Case is folded.** `photo.JPG` and `photo.jpg` both land in `Images/jpg/`. The filename itself keeps its original case.
- **Dotfiles have no extension.** `.gitignore` goes to `Other/no-extension/`, not `Other/gitignore/`.
- **Compound suffixes stay whole.** `backup.tar.gz` goes to `Archives/tar.gz/`, so tarballs group as tarballs rather than splitting by compressor into `gz/` and `bz2/`.

Pooled layout resolves name collisions with the same `_1`, `_2` suffix rule as flatten mode. Collisions are detected **case-insensitively**, because macOS, Windows, and every exFAT/FAT32 external drive treat `beach.JPG` and `Beach.jpg` as one file — so `Trip/beach.JPG` and `Work/Beach.jpg` become `beach.JPG` and `Beach_1.jpg` rather than one overwriting the other. Filenames keep their original case.

The "keep original folders" layout cannot collide at all: a file's path relative to the source is already unique.

Classification is by filename, not content: a `.pdf` renamed to `.txt` is sorted as text.

## Application Structure

```
SuperDirectory/
├── main.go              # Wiring: wizard → planner → copier, progress bar, inspect
├── go.mod / go.sum      # Module definition and dependency checksums
├── internal/
│   ├── flatten/         # Functional core: shared walk, flat planner, the copier
│   ├── organize/        # Second planner: Category/extension layout
│   ├── pick/            # Keyboard directory browser
│   ├── exclude/         # Recursive exclusion tree (Bubble Tea)
│   ├── wizard/          # Interactive layer (Charm huh)
│   └── extract/         # Content-inspection seam
├── legacy-python/       # The original Python script, superseded
├── roadmap.md           # Direction, decisions, and what's next
├── UX-Adjustment-History.md
├── LICENSE              # MIT License
└── README.md            # This file
```

### Code Organization

| Package | Role | Notes |
|---|---|---|
| `internal/flatten` | Shared tree walk, the flat planner, and the copier | No TUI knowledge. Pure and unit-tested. |
| `internal/organize` | The second planner: sort into `Category/extension/` | Shares `flatten.Walk`, emits `flatten.Item`, executed by `flatten.Copy`. |
| `internal/pick` | Keyboard directory browser | Handles source and destination selection. |
| `internal/exclude` | Recursive exclusion tree on Bubble Tea | Descend to any depth; excluding a directory skips its whole subtree. |
| `internal/wizard` | Interactive layer on Charm `huh` | Mode → source → destination → exclusions → layout → confirm. Returns a plain `Result`. |
| `internal/extract` | Content-inspection seam | Pure-Go `MetadataExtractor` today; `PythonExtractor` is the documented future plug-in. |
| `main.go` | Wiring + progress bar + `inspect` demo | lipgloss styling. |

The separation is deliberate. The core has zero dependency on the interactive layer, so it stays fast and testable, and the TUI can be swapped or driven from tests.

Both planners emit the same `[]flatten.Item` — a source path plus a destination *relative path* — and both are executed by the same `flatten.Copy`, which creates parent directories on demand. Adding a third layout means adding a planner, not touching the copier. Sharing `flatten.Walk` means exclusion semantics can never drift between them.

### The polyglot seam

`extract.Extractor` is one interface with two backends:

- `MetadataExtractor` — pure Go, zero deps. Sniffs MIME type from content, derives a title from the filename. Ships today.
- `PythonExtractor` — the future. When deep extraction (PDF text, DOCX bodies, OCR) is needed, it shells out to an **optional Python helper process** and decodes JSON. Because the coupling is process-level rather than libpython/CGO, the Go binary stays a clean static file and cross-compilation is unaffected. Not wired yet — see the blueprint comment in `extract.go`.

## Development

```bash
go test ./...        # core semantics: prefixing, classification, collisions, exclusion, copy
go vet ./...
gofmt -l .           # silence means clean
```

Cross-compile a static binary for any OS from any OS, no toolchain required:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o sd-windows-amd64.exe .
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o sd-linux-amd64 .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o sd-darwin-arm64 .
```

Packaging and signing (GoReleaser + Developer ID notarization) are not wired up yet. See the [roadmap](roadmap.md).

## License

[MIT](LICENSE)
