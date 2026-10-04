# SuperDirectory

A terminal program that copies a nested folder tree into one **superdirectory**: one flat folder, a folder per file type, a folder per month a photo was taken, or the top folder levels kept with everything below them pooled. The source folders are never changed. Every file is copied.

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

**Organize by date taken** sorts photos and videos into `year/month/`, by the date in their metadata. Files without one go by their modification date.

```
photos-super/
├── 2019/
│   └── 07/
│       ├── IMG_0412.HEIC
│       └── IMG_0413.MOV
└── 2024/
    └── 12/
        └── DSC_0001.NEF
```

**Keep the top levels** keeps the first N folder levels and flattens everything below them. With N = 1, each top folder becomes one flat folder:

```
manuals-super/
├── Printers/
│   ├── setup.pdf
│   └── drivers_readme.txt
└── Scanners/
    └── models_s400_guide.pdf
```

## Why

1. I created this utility to help me skim through scrapes of proprietary/obscure technical documentation. I use these scrapes to assist in my work troubleshooting and solution engineering communication between aging hardware with modern networking infrastructure. These databases are often nested in tens or hundreds of subdirectories, many of which are unrelated to products I work with or are aged out of relevancy. This utility allows me to sort through directories with ease while keeping a backup of the entire documentation system structure if needed. Copying these files into a SuperDirectory allows for easy multi-file uploads to services such as NotebookLM to ask questions of the database without including irrelevant documents.
2. This project was built partially as a UX design showcase using Claude Code. The included [UX Adjustment History](UX-Adjustment-History.md) document showcases follow-up prompts during testing of the application to make it as user-friendly and fun to use as possible.

It began as a single-file Python script. It was [rewritten in Go](roadmap.md) to become a zero-runtime single binary that works on every OS, and to leave room for the CPU-bound features on the roadmap. The original script is preserved in [`legacy-python/`](legacy-python/) as the UX reference.

## Features

**Layouts**
- Flat, by file type, by date taken, or with the top N folder levels kept
- Several source folders in one run, each kept apart by name in the layouts that keep folders
- Editable type categories: `superdirectory categories --write`, then edit the JSON file

**Choosing what goes in**
- An exclusion tree: skip any subfolder, at any depth, with a preview of its contents
- Filters by type or extension, by name pattern (`*.tmp`, `node_modules`), by size, and by modification date
- Saved presets, and a `copy` command with flags for scripts

**Duplicates**
- Byte-identical files: one of each set is copied
- Smaller copies of a picture: the largest is copied
- Near-duplicate documents, such as revisions: the newest is copied
- A review screen that shows each set, with thumbnails of pictures, before anything is skipped

**Documents and scrapes**
- Names documents after their own titles
- Detects each file's type from its content, and corrects a wrong or missing extension
- Expands `.zip`, `.tar` and `.chm` files found in the sources
- Merges text documents into a few large Markdown files
- Splits the output into batch folders, such as 50 files each for NotebookLM

**Big copies and flaky drives**
- Live throughput, and a time-left estimate that says how sure it is
- Pause and resume a copy; stop it and finish it later
- Verify each copy against its source
- Instant clones when the source and the destination share an APFS, Btrfs or XFS volume
- A free-space check before the copy starts
- A file that stops delivering data is abandoned after 60 s, and the copy carries on; failed files are retried at the end
- Works on exFAT and FAT32, skips the metadata macOS and Windows leave on a drive, and preserves modification times

**Records**
- A report in the superdirectory: every file's origin, and every skip and failure with its reason
- A desktop notification when a long copy finishes

## Installation

### Homebrew (macOS and Linux)

```bash
brew install ozzyphantom/tap/superdirectory
```

### Download

Download the archive for your system from [GitHub Releases](https://github.com/ozzyphantom/SuperDirectory/releases/latest): macOS, Linux and Windows, each for amd64 and arm64. Each archive holds one file, the `superdirectory` program. Put it in a folder on your `PATH`. `checksums.txt` lists the SHA-256 of every archive.

### Go

```bash
go install github.com/ozzyphantom/SuperDirectory@latest
SuperDirectory
```

`go install` names the program after the module, so it is `SuperDirectory` with capitals. macOS and Windows accept any case. Linux does not.

### Build from source

Go 1.26 or newer is needed **only to build**. The binary has no runtime dependencies. An older `go` downloads a supported toolchain on its own; `go.mod` pins the one releases are built with.

```bash
git clone https://github.com/ozzyphantom/SuperDirectory.git
cd SuperDirectory
go build -o superdirectory .
./superdirectory
```

## Usage

Run it with no arguments to start the wizard. The wizard needs a real terminal. Without one, it exits with status 1 and says so.

```bash
superdirectory
```

### The wizard

Every step has a back option, and `Esc` goes back a step. Steps that do not apply are skipped.

| Step | Asks |
|---|---|
| Preset | Start from a saved preset? Shown only when presets exist |
| Layout | Flat, by type, by date taken, or keep the top levels |
| Sources | One or more folders to copy from. A folder inside another chosen source is refused |
| Destination | Where the superdirectory goes, and its name. A folder with an interrupted copy offers to resume it |
| Exclusions | Subfolders to skip, in a tree for each source |
| Detail | By type: keep the original folders? Keep the top levels: how many, 1 to 5 |
| Filters | Every file, or choose types, extensions, name patterns, sizes and dates |
| Duplicates | Identical files, smaller copies of pictures, near-duplicate documents, and whether to review the sets first |
| Documents | Titles, types by content, expand archives, merge text, batches (50, 100, 300 or 600 files) |
| Options | Write a report, retry failed files once, verify copies, notify when done |
| Confirm | Copy, save the settings as a preset, go back, or cancel |

### Keys

| Where | Key | Does |
|---|---|---|
| Everywhere | `Ctrl+C` | Quit |
| Every wizard step | `Esc` | Go back a step |
| Menus | `↑` / `↓`, `Enter` | Move, choose |
| Checklists | `Space` | Tick or untick |
| Directory browser | `→` | Open the highlighted folder |
| Directory browser | `←` / `Backspace` | Go up to the parent folder |
| Directory browser | any letter | Jump to the next entry starting with it |
| Directory browser | `Enter` | Choose the current folder |
| Exclusion tree | `Space` | Exclude or include the highlighted folder |
| Exclusion tree | `→` / `←` | Expand or collapse a folder |
| Exclusion tree | `p` | Preview the highlighted folder |
| Exclusion tree | `Enter` | Accept the exclusions |
| Duplicate review | `←` / `→` | Previous or next set |
| Duplicate review | `↑` / `↓`, `Enter` | Choose the file to copy from a set |
| Duplicate review | `a` | Copy every file of the set, or go back to one |
| Duplicate review | `n` | Next set that still skips a file |
| Duplicate review | `d` | Done: copy as chosen |
| Duplicate review | `Esc` | Back to the duplicates menu |
| During a copy, in the wizard | `p` | Pause, and press again to resume |
| During a scan or copy | `Ctrl+C` | Stop cleanly. Press again to quit at once |

The directory browser moves on the arrow keys, so every letter stays free for type-to-jump. The exclusion tree and the review screen also take `h` `j` `k` `l`.

### The command line

```
superdirectory copy [flags]        copy without the wizard
superdirectory resume TARGET       finish an interrupted copy into TARGET
superdirectory presets             list saved presets; presets delete NAME removes one
superdirectory categories          print the type categories; --write saves them for editing
superdirectory inspect DIR         show each file's detected type and title
superdirectory --help | --version
```

`copy` takes the same settings as the wizard, as flags. `superdirectory --help` lists them all. Two examples:

```bash
# A documentation scrape, ready for NotebookLM
superdirectory copy --from ~/scrapes/vendor-docs --to ~/NotebookLM/vendor \
  --duplicates documents --rename-titles --detect-types --expand --merge-text --batch 50

# A photo drive, by date taken, skipping duplicates and verifying every copy
superdirectory copy --from /Volumes/Card --to /Volumes/Archive/Photos \
  --layout date --duplicates identical,pictures --verify --notify
```

`copy` shows the settings and asks before copying. `--yes` skips the question; without a terminal, `--yes` is required. `--preset NAME` starts from a saved preset, and any flag given beside it overrides that setting. `--save-preset NAME` saves the settings the command ran with.

`copy` refuses a destination that holds files, unless it holds an interrupted copy, which it resumes.

Exit status: 0 done, 1 an error or files that failed, 2 bad usage, 130 stopped.

### Presets

The wizard's last step can save its settings as a preset, and the first step offers the saved ones. Each preset is a JSON file in SuperDirectory's settings folder:

| System | Folder |
|---|---|
| macOS | `~/Library/Application Support/SuperDirectory/presets/` |
| Linux | `$XDG_CONFIG_HOME/SuperDirectory/presets/`, by default `~/.config/SuperDirectory/presets/` |
| Windows | `%AppData%\SuperDirectory\presets\` |

## How files are classified

Organize mode sorts each file into `Category/extension/`. Built-in categories: Archives, Audio, Code, Diagrams, Documents, Fonts, Images, Presentations, Spreadsheets, Video, and `Other/` for everything else.

Details worth knowing:

- **Case is folded.** `photo.JPG` and `photo.jpg` both land in `Images/jpg/`. The filename itself keeps its original case.
- **Spellings of one type share a folder.** `.jpeg` files land in `Images/jpg/`, `.htm` in `Code/html/`, `.yml` in `Code/yaml/`. The file keeps its name.
- **Dotfiles have no extension.** `.gitignore` goes to `Other/no-extension/`, not `Other/gitignore/`.
- **Compound suffixes stay whole.** `backup.tar.gz` goes to `Archives/tar.gz/`, so tarballs group as tarballs rather than splitting by compressor into `gz/` and `bz2/`.

Pooled layouts resolve name collisions with the same `_1`, `_2` suffix rule as flatten mode. Collisions are detected **case-insensitively**, because macOS, Windows, and every exFAT/FAT32 external drive treat `beach.JPG` and `Beach.jpg` as one file — so `Trip/beach.JPG` and `Work/Beach.jpg` become `beach.JPG` and `Beach_1.jpg` rather than one overwriting the other. Filenames keep their original case.

The "keep original folders" layout cannot collide at all: a file's path relative to the source is already unique.

Classification is by filename unless types are detected by content (see [Detect types by content](#detect-types-by-content)).

### Editing the categories

```bash
superdirectory categories          # print the table in use
superdirectory categories --write  # save it as categories.json for editing
```

`categories.json` sits in the settings folder beside `presets/`. While it exists, it replaces the built-in table. It has two sections: `categories`, each category with its extensions, and `aliases`, each spelling with the folder it shares. Delete the file to go back to the built-in table.

## Choosing what goes in

Filters apply while the sources are walked, so a filtered-out file is never read.

| Filter | Wizard | Flag |
|---|---|---|
| Types or extensions to copy | Filters → choose types | `--only Documents,png` |
| Types or extensions never to copy | Filters → untick a type | `--not Video,iso` |
| Names to skip, files or folders | Filters → name patterns | `--skip '*.tmp' --skip node_modules` |
| Smallest and largest size | Filters → sizes | `--min-size 10KB --max-size 200MB` |
| Modified on or after, on or before | Filters → dates | `--since 2024-01-01 --until 2024-12-31` |
| Folders to leave out | Exclusions | `--exclude DIR` |

Sizes take `KB`, `MB`, `GB` and `TB` (powers of 1,000) and `KiB`, `MiB`, `GiB` and `TiB` (powers of 1,024). Patterns match names, not paths, and ignore case.

## What gets skipped

Symlinks, sockets, and devices are never copied — real files only.

Filesystem bookkeeping is skipped too, in every layout. This matters most on external drives, where a Mac writes an invisible `._` sidecar next to every file: flattening a drive root can otherwise produce a superdirectory where the metadata outnumbers your files.

| Skipped | Where it comes from |
|---|---|
| `._*` (AppleDouble sidecars), `.DS_Store`, `.localized` | macOS, on any volume it touches |
| `.Spotlight-V100/`, `.fseventsd/`, `.Trashes/`, `.DocumentRevisions-V100/` | macOS drive services |
| `__MACOSX/` | macOS, inside zip files it made |
| `Thumbs.db`, `desktop.ini`, `$RECYCLE.BIN/`, `System Volume Information/` | Windows |
| `.Trash/`, `.Trash-1000/` | Linux |
| `.superdirectory/` | SuperDirectory's own record folder, in an earlier superdirectory |

Dotfiles you wrote are *not* metadata: `.gitignore`, `.env`, and `.git/` are copied normally. And if you deliberately choose a metadata directory as your source — say you point at `.Trashes` to recover something — its contents are copied.

Modification times are preserved, so an archived superdirectory remembers when its files were written rather than claiming they all appeared today. Permissions are preserved where the destination filesystem can store them; exFAT and FAT32 cannot, and will show their mount-wide defaults.

macOS can still write `._` files *into* a superdirectory on an exFAT or FAT32 drive, when it attaches extended attributes to the new files. Finder hides them. `dot_clean -m FOLDER` removes them.

## Duplicate detection

Optional, and off by default, because it reads files. The duplicates step offers three scans. They run in order, each over the files the one before kept.

### Identical files

SuperDirectory finds files whose contents are **byte-for-byte identical**, whatever they are named, and offers to copy one of each set.

Identity is decided in three stages, so most files are never opened:

1. **Size.** Files of different sizes cannot be identical. Most photographs have a unique byte size — those are settled with one `stat` each.
2. **A 64 KiB partial hash**, for files that share a size. Two different photographs of the same size almost always differ in their first block.
3. **A full hash**, only for the few whose leading block also matched.

On a folder of eleven thousand photographs this typically reads a few hundred.

Which copy survives:

1. A name that does not read as a copy. `beach copy.jpg`, `beach - Copy.jpg`, `beach (1).jpg`, `Copy of beach.jpg`, and `beach_1.jpg` beside `beach.jpg` all lose to `beach.jpg`.
2. Then the shallowest path, nearest the top of the source.
3. Then walk order, which is lexical.

So `Trip/beach.jpg` survives over `Backup/beach copy.jpg`, even though `Backup` sorts first. After the duplicates are dropped, names are assigned again, so a survivor never keeps a `_1` suffix it only needed beside its twin.

A file that cannot be read is never called a duplicate; it is reported and copied.

### Smaller copies of pictures

The same picture saved at a lower resolution: a photo and its web export, a diagram and its thumbnail. The largest copy is kept. Pictures are JPEG, PNG, GIF, BMP, TIFF, and WebP everywhere, and HEIC on a Mac, through the built-in `sips`.

```
┃ Found 5 identical file(s) and 40 smaller copies of pictures, 7.3 MB
┃ Identical: the same contents, whatever the name. One of each set is
┃ copied: the name that is not a copy, nearest the top of the source.
┃
┃ Smaller copies: the same picture at a lower resolution. The largest
┃ is copied. For example:
┃   DSC_0001.png 480×320  →  DSC_0001.JPG 2400×1600
┃   DSC_0012-web.jpg 1200×800  →  DSC_0012.JPG 2400×1600
┃   DSC_0024-web.jpg 1200×800  →  DSC_0024.JPG 2400×1600
┃   … and 37 more
┃ > Skip them all
┃   Choose which kinds to skip…
┃   Review them one by one…
┃   Copy everything
┃   Cancel
```

Each picture is shrunk to a 32×32 grid and fingerprinted. Two pictures match when they have the same shape, different pixel counts, and nearly the same fingerprint. Three rules keep the scan from skipping a picture you meant to keep:

- **A copy at the same size is never skipped.** Frames from one burst share a size, and a frame nudged by 1% looks exactly like a resized copy to any fingerprint. Only a smaller copy defers to a larger one.
- **RAW files are never compared.** A NEF, CR2, or DNG and the JPEG made from it are both kept. RAW files are still matched when byte-identical.
- **A match found through an embedded thumbnail is confirmed against the full pictures.** An editor that crops a photo can leave the original's thumbnail behind; the full decode catches it.

Most pictures cost one header read. A camera JPEG's header holds its size, its orientation, and a small thumbnail that is fingerprinted on the spot. A picture is decoded in full only if another picture of the same shape has a different size, and only when it has no usable thumbnail. A library straight off one camera, with no resized copies, stops at the headers. Pictures under 64 pixels on a side, and blank or near-uniform ones, are not compared.

### Near-duplicate documents

Documents that are mostly the same text: revisions of a manual, a page saved twice by a scraper, a web page and its source. The newest is kept. On one date, the larger file is kept, then the shorter path.

Each document's text is read — plain text, Markdown, HTML, RTF, Word, ODT, EPUB and PDF — and broken into overlapping runs of five words. Two documents match when about 80% of their runs are shared. A document is skipped only when it matches the one kept, so a chain of revisions where the first and the last barely match never skips the last on the middle one's account. Documents under 200 words are not compared: short pages that are mostly a site's shared menus look alike to any measure of text.

### Reviewing duplicates

Choose "Review them one by one" at the duplicates menu, or tick "review the sets before anything is skipped" in the wizard, or pass `--review`. The review screen shows each set: every file, its size and dimensions or similarity, and which one is copied. Pictures show as thumbnails side by side, drawn in text, in any terminal with color.

In a set, choose a different file to copy, or copy every file of the set. `d` copies as chosen. In the wizard, `Esc` goes back to the duplicates menu, where nothing has been skipped yet. With `copy --review`, `Esc` cancels the copy.

## Documents and scrapes

These are off by default. Turn them on in the wizard's Documents step, or with their flags.

### Name documents after their titles

`--rename-titles`. A document whose name says nothing, such as `doc_4417.pdf` or `index.html`, is copied under its own title: `Configuring VLANs.pdf`. The title comes from the document's metadata, or its first heading. The name keeps the original extension, and the report keeps the original name.

### Detect types by content

`--detect-types`. The start of each file is read. A file whose content disagrees with its name takes the extension its content shows: a HEIC photo named `.jpg` becomes `.heic`, and a PDF with no extension becomes `.pdf`. The layout sorts it by what it is.

- Text fits any name a binary format does not claim, so `README`, `go.mod` and subtitle files keep their names.
- HTML under a server page's extension, such as `.php`, `.asp` or `.jsp`, is a saved page, and becomes `.html`. A page that still holds server code (`<?php`, `<%`) is source, and keeps its name.
- A file is never renamed to a program or package, such as `.exe`, `.jar` or `.apk`.

### Expand archives

`--expand`. Archives found in the sources are unpacked, and their files take the archive's place: `manual.zip` becomes a `manual/` folder. Formats: `.zip`, `.tar`, `.tar.gz` (`.tgz`), `.tar.bz2` (`.tbz2`), and `.chm`, the compiled help format of Windows.

- One level only. An archive inside an archive is copied, not unpacked.
- Filters and exclusions apply to the files inside.
- An archive that cannot be unpacked — damaged, encrypted, or over the limits — is copied whole, and the report says why. The limits: 100,000 files, 20 GiB, and a size no more than 200 times the archive's (plus 100 MB), which stops a zip bomb.
- Links, absolute paths and paths that climb out of the archive are refused.

### Merge text documents

`--merge-text`. The text documents of each destination folder join into a few Markdown files, `<folder> 001.md` and on. In the flat layout, the files take the superdirectory's name. Each document sits under a heading naming where it came from. Each file stays under 400,000 words and 150 MB, inside NotebookLM's limits of 500,000 words and 200 MB per source.

- Merged: plain text, Markdown, reStructuredText, AsciiDoc, Org, TeX, subtitles, HTML, RTF, Word, ODT and EPUB.
- Not merged: PDFs, which NotebookLM reads with their figures, and code, settings and tables.
- A folder with one document keeps it as it is. A document with no readable text is copied whole.

### Batches

`--batch N`. The output is split into folders of at most N files, `Batch 01`, `Batch 02` and on, in walk order, each keeping its layout inside. NotebookLM's free plan takes 50 sources per notebook, so `--batch 50` makes one folder per notebook. The report lists any file over NotebookLM's 200 MB per source.

## Copying

The copy screen shows the progress bar, the files and bytes done, the current rate, the time left, and the file in flight:

```
  Copying 3,004 files (8.0 GB) into /Volumes/Archive/Photos

  [████████████████░░░░░░░░]  69%  1,879/3,004  5.6 GB  419.4 MB/s  ~6s left, rough  IMG_1875.jpg

  p pause   ctrl+c stop
```

### Time left

The estimate says how sure it is:

| Shown | Means |
|---|---|
| `estimating…` | The first 5 seconds, or the first 1% of the bytes. Too early for a number |
| `~4m left, rough` | The rate is still changing |
| `~4m left` | The rate has held within 20% for ten seconds |

It counts bytes, not files, so one large video does not throw it off. The rate is recent, not an average over the whole copy, so a drive that slows down shows it.

### Pause

Press `p` during a copy in the wizard. The copy holds at once, part way through a large file if need be. The line shows `paused` and for how long. Press `p` again to resume. Paused time does not count against the rate, the time left, or the 60-second stall limit. Pausing lets a hot drive cool.

### Stop and resume

`Ctrl+C` during a scan or the copy stops cleanly. The file in flight is abandoned, and its partial destination is deleted, so the superdirectory never holds a truncated file under a real file's name. Files already copied stay.

```
  Stopped.  454 of 1,504 files copied into /Volumes/Archive/Photos-super
  The partial copy of IMG_0455.jpg was removed.
  Run it again into the same folder to resume where it stopped.
```

To resume, choose the same destination in the wizard, which offers to resume, or run `superdirectory resume TARGET`. The settings come from the stopped run. A file already at its destination with the same size and modification time is not copied again.

The exit status of a stopped copy is 130, the shell convention for an interrupted program.

### Verify

`--verify`, or Options → verify copies. Each copy is read back from the destination and its SHA-256 compared with the source's. A copy that differs is deleted and counted as failed, and the retry copies it again.

### Instant copies on one volume

When the source and the destination are on the same APFS volume (macOS), or the same Btrfs or XFS volume (Linux), each file is cloned instead of copied. A clone takes no time and no space until one side changes. The summary says "cloned on the same volume, no data copied".

### Free space

Before the copy starts, the bytes to copy are compared with the destination's free space. When they do not fit, the wizard asks whether to copy anyway, and `copy` refuses unless `--yes` is given. A copy that fills the destination stops at the first file that does not fit, removes that file's partial copy, and says so. Free some space, then resume.

### Retry

Files that failed are tried again at the end of the copy, once by default. `--retries N` sets the number of attempts, and the wizard asks before each one. A file that stalled on a hot drive often reads after a rest.

### When a file will not read

A copy does not hang on one bad file. If a file delivers no data for 60 seconds, it is abandoned, its partial destination is deleted, it is recorded in the failures list, and the copy carries on.

```
  [██░░░░░░░░]  9%  1,084/11,041  1.2 GB  — MB/s  ⚠ no data for 47s  DSC_4418.NEF
```

The read itself cannot be interrupted. Go's `SetReadDeadline` works only on pipes and sockets, never on a regular file, and no syscall unblocks a read parked in a disk retry. The file's copy runs on its own goroutine and is *abandoned* — a deliberate, bounded leak that beats hanging the whole program on one bad sector. The scans read through the same guard.

## The report

Each superdirectory gets a `.superdirectory/` folder:

| File | Holds |
|---|---|
| `report.md` | The run at a glance: settings, counts, and the failed, skipped, merged and renamed files |
| `report.csv` | One row for every file the run considered: status, source, destination, bytes, and a note. Status is one of copied, cloned, already there, expanded, skipped, merged, failed, not reached |
| `job.json` | The run's settings, and whether it finished. `resume` reads it |

Files from an archive show their origin as `archive.zip!/inner/path.htm`. `--no-report`, or unticking the report in Options, leaves out the two reports. `job.json` is written either way while the copy runs, so a stopped copy can resume; a copy that finishes without a report removes it, and the whole `.superdirectory/` folder with it.

## Notifications

`--notify`, or Options → notify. When the copy ends, a desktop notification says how many files arrived, how many failed, and whether the destination filled up. It uses `osascript` on macOS, `notify-send` on Linux, and PowerShell on Windows.

## Application Structure

```
SuperDirectory/
├── main.go              # Commands, flags, and the wizard loop
├── internal/            # The packages below
├── site/                # The product website (Astro, static)
├── legacy-python/       # The original Python script, superseded
├── .goreleaser.yaml     # Release builds, archives, and the Homebrew cask
├── RELEASING.md         # How to publish a release
├── roadmap.md           # Direction, decisions, and what's next
├── UX-Adjustment-History.md
└── LICENSE              # MIT License
```

| Package | Role |
|---|---|
| `internal/job` | A run's settings, validation, presets, and the settings folder |
| `internal/engine` | Runs a job through its stages: walk, inspect, expand, plan, duplicates, merge, batch, copy, report |
| `internal/flatten` | The shared walk, the flat and depth planners, and the copier: guarded reads, pause, resume, verify, clones |
| `internal/organize` | The type and date planners, and the editable category table |
| `internal/filter` | Type, pattern, size and date filters, applied during the walk |
| `internal/fsmeta` | Which names are filesystem bookkeeping |
| `internal/guard` | Reads that give up on a stalled file instead of hanging |
| `internal/dedup` | Identical files and smaller copies of pictures |
| `internal/textdup` | Near-duplicate documents, by MinHash over word runs |
| `internal/exif` | Dimensions, orientation, thumbnails and capture dates from JPEG, HEIC, TIFF-based RAW, RAF, CR3, PNG, WebP and video headers |
| `internal/sniff` | A file's type from its content |
| `internal/textual` | A document's text: plain text, HTML, RTF, Word, ODT, EPUB, and PDF |
| `internal/title` | A document's own title, from its metadata or first heading |
| `internal/extract` | What `inspect` shows: type, title and capture date, behind the extractor seam the [roadmap](roadmap.md) describes |
| `internal/merge` | Packs documents into Markdown files under a word and byte limit |
| `internal/expand` | Unpacks zip and tar archives, with limits |
| `internal/chm` | Reads compiled help (`.chm`) files, LZX included |
| `internal/review` | The duplicate review screen |
| `internal/notify` | Desktop notifications |
| `internal/ui` | The copy screen, status lines, prompts, and the plain output of `copy` |
| `internal/wizard` | The wizard's steps, on Charm `huh` |
| `internal/pick` | The keyboard directory browser |
| `internal/exclude` | The recursive exclusion tree, on Bubble Tea |
| `internal/hint` | Key hints fitted to the terminal's width |

The engine knows nothing about screens. It reports progress and asks its questions through a `Hooks` interface, which the wizard answers with menus and the `copy` command answers from its flags. Every layout emits the same `[]flatten.Item` — a source path and a destination relative path — and one copier executes them all.

## Development

```bash
go test ./...        # unit tests for every package
go test -race ./...
go vet ./...
gofmt -l .           # silence means clean
```

The format readers have fuzz tests. Run one with `go test -fuzz FuzzName ./internal/PACKAGE`.

Cross-compile a static binary for any OS from any OS, no toolchain required:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o sd-windows-amd64.exe .
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o sd-linux-amd64 .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o sd-darwin-arm64 .
```

Releases are built by GoReleaser from a version tag. [RELEASING.md](RELEASING.md) has the steps, including signing and notarizing for macOS.

## License

[MIT](LICENSE)

---

The "Why" section is Oscar Garcia's. Claude (AI) wrote the rest of this file from the code and its tests.
