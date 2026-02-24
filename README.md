# SuperDirectory

An interactive CLI tool that flattens a nested directory tree into a single "super directory" — all files from subdirectories are copied into one flat folder, with filenames prefixed by their parent directory name to avoid collisions.

## What It Does

Given a source directory like this:

```
my-project/
├── src/
│   ├── main.py
│   └── utils.py
├── tests/
│   ├── test_main.py
│   └── test_utils.py
└── README.md
```

SuperDirectory produces:

```
my-project-super/
├── README.md
├── src_main.py
├── src_utils.py
├── tests_test_main.py
└── tests_test_utils.py
```

Files in the root of the source directory keep their original names. Files from subdirectories are prefixed with `parentdir_` to maintain context and prevent naming conflicts. If a collision still occurs, a numeric suffix (`_1`, `_2`, ...) is appended.

## Features

- **Interactive wizard** — guided step-by-step prompts for source, destination, and exclusions
- **Exclusion system** — selectively skip subdirectories at any depth with a full navigation wizard
- **Directory preview** — inspect directory contents before deciding to include or exclude
- **Custom checkbox widget** — built on `prompt_toolkit` with keyboard shortcuts for fast selection
- **Select all / deselect all** — press `a` in the checkbox to toggle all items at once
- **Path autocomplete** — tab-completion when entering source and destination paths
- **Progress bar** — visual feedback during the copy operation
- **Undo / go back** — navigate backward through exclusion choices or start over entirely
- **Keyboard shortcuts** — `y`/`n` for yes/no prompts, single-key shortcuts in all menus

## Requirements

- Python 3.8+
- [questionary](https://github.com/tmbo/questionary) (terminal UI prompts)
- [prompt_toolkit](https://github.com/prompt-toolkit/python-prompt-toolkit) (custom checkbox widget and path completion)

## Installation

```bash
git clone https://github.com/ozzyphantom/SuperDirectory.git
cd SuperDirectory
python -m venv venv
source venv/bin/activate  # On Windows: venv\Scripts\activate
pip install -r requirements.txt
```

## Usage

```bash
python SuperDirectory.py
```

The script walks you through five steps:

1. **Source Directory** — use the current directory or enter a path
2. **Destination** — choose where to save and name the output directory
3. **Exclusion** — optionally exclude subdirectories via an interactive wizard
4. **Confirmation** — review your choices before copying
5. **Copying** — files are copied with a progress bar

### Keyboard Controls

| Context | Key | Action |
|---|---|---|
| Everywhere | `Ctrl+C` | Exit the program |
| Yes/No prompts | `y` / `n` | Jump to Yes or No |
| All menus | `↑` / `↓` | Navigate choices |
| All menus | `Enter` | Confirm selection |
| Checkbox | `Space` | Toggle a single item |
| Checkbox | `a` | Select / deselect all |
| Checkbox | `p` | Preview highlighted directory |
| Checkbox | `Esc` | Go back to previous menu |

## Application Structure

```
SuperDirectory/
├── SuperDirectory.py    # Entire application (single-file script)
├── requirements.txt     # Python dependencies
├── LICENSE              # MIT License
├── .gitignore           # Excludes venv/ and __pycache__/
└── README.md            # This file
```

### Code Organization (`SuperDirectory.py`)

The script is organized into clear sections separated by comment banners:

| Section | Lines | Purpose |
|---|---|---|
| **Imports** | Top | Standard library + questionary + prompt_toolkit |
| **ANSI helpers** | `green()`, `red()`, `cyan()`, `bold()`, `dim()` | Terminal color formatting for `print()` output |
| **Styles** | `STYLE`, `CHECKBOX_STYLE` | Theme definitions for questionary and prompt_toolkit |
| **Helpers** | `ask()`, `yn_select()`, `section()`, `get_subdirs()` | Reusable prompt utilities |
| **Custom checkbox** | `run_directory_checkbox()` | A prompt_toolkit `Application` that renders an interactive checkbox list with toggle, select-all, preview, and escape support |
| **ExclusionWizard** | `ExclusionWizard` class | Stateful wizard that recursively walks subdirectories, letting the user skip/keep/preview at each level with full undo support |
| **Main flow** | Bottom half | Sequential prompts for source → destination → exclusion → confirmation → copy |

### Key Design Decisions

- **Single file** — the entire tool is one self-contained script with no internal module structure, making it easy to download and run
- **Custom checkbox over questionary's built-in** — questionary's checkbox doesn't support preview, select-all, or escape-to-go-back, so a custom widget was built directly on `prompt_toolkit`
- **Set-based exclusion tracking** — the `ExclusionWizard` tracks excluded paths in a `set` for O(1) membership tests and clean set operations (`|=`, `-=`)
- **Snapshot-based undo** — before descending into subdirectories, the wizard snapshots the exclusion set so it can be fully restored on "go back"
- **ANSI colors for print, questionary styles for prompts** — ANSI escape codes are only used in `print()` output; questionary prompts use the `Style` system to avoid rendering issues

## License

[MIT](LICENSE)
