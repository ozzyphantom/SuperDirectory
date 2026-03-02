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

## Why

1. I created this utility to help me skim through scrapes of proprietary/obscure technical documentation. I use these scrapes to assist in my work troubleshooting and solution engineering communication between aging hardware with modern networking infrastructure. These databases are often nested in tens or hundreds of subdirectories, many of which are unrelated to products I work with or are aged out of relevancy. This utility allows me to sort through directories with ease while keeping a backup of the entire documentation system structure if needed. Copying these files into a SuperDirectory allows for easy multi-file uploads to services such as NotebookLM to ask questions of the database without including irrelevant documents. 
2. This script was built partially as a UX design showcase using Claude Code. The included [UX Adjustment History](UX-Adjustment-History.md) document showcases follow up prompts during testing of the application to make this script as user-friendly and fun to use as possible. 

## Features

- **Interactive wizard** — guided step-by-step prompts for source, destination, and exclusions
- **Exclusion system** — selectively skip subdirectories at any depth with a full navigation wizard
- **Directory preview** — inspect directory contents before deciding to include or exclude
- **Path autocomplete** — tab-completion with arrow-key navigation when entering source and destination paths

## Requirements

- Python 3.8+
- [questionary](https://github.com/tmbo/questionary) (terminal UI prompts)
- [prompt_toolkit](https://github.com/prompt-toolkit/python-prompt-toolkit) (custom checkbox widget and path completion)

## Installation

```bash
git clone https://github.com/ozzyphantom/SuperDirectory.git
cd SuperDirectory
```

## Usage

The easiest way to run SuperDirectory is with the launcher script, which handles virtual environment setup and dependency installation automatically:

```bash
./start.sh
```

Alternatively, set up manually and run directly:

```bash
python -m venv venv
source venv/bin/activate  # On Windows: venv\Scripts\activate
pip install -r requirements.txt
python SuperDirectory.py
```

The script walks you through five steps:

1. **Source Directory** — use the current directory or enter a path
2. **Destination** — choose where to save and name the output directory
3. **Exclusion** — optionally exclude subdirectories via an interactive wizard
4. **Confirmation** — review your choices; change destination, revisit exclusions, or restart
5. **Copying** — files are copied

### Keyboard Controls

| Context | Key | Action |
|---|---|---|
| Everywhere | `Ctrl+C` | Exit the program |
| Yes/No prompts | `y` / `n` | Jump to Yes or No |
| All menus | `↑` / `↓` | Navigate choices |
| All menus | `Enter` | Confirm selection |
| Path input | `Tab` / arrow keys | Autocomplete directories |
| Checkbox | `Space` | Toggle a single item |
| Checkbox | `a` | Select / deselect all |
| Checkbox | `p` | Preview highlighted directory |
| Checkbox | `Esc` | Go back to previous menu |

## Application Structure

```
SuperDirectory/
├── SuperDirectory.py    # Entire application (single-file script)
├── start.sh             # Launcher: creates venv, installs deps, runs script
├── requirements.txt     # Python dependencies
├── LICENSE              # MIT License
├── .gitignore           # Excludes venv/ and __pycache__/
└── README.md            # This file
```

### Code Organization (`SuperDirectory.py`)

| Section | Lines | Purpose |
|---|---|---|
| **Imports** | Top | Standard library + questionary + prompt_toolkit |
| **ANSI helpers** | `green()`, `red()`, `cyan()`, `orange()`, `bold()`, `dim()` | Terminal color formatting for `print()` output |
| **Styles** | `STYLE`, `CHECKBOX_STYLE` | Theme definitions for questionary and prompt_toolkit |
| **Helpers** | `ask()`, `yn_select()`, `section()`, `get_subdirs()`, `ask_directory()`, `ask_dirname()` | Reusable prompt utilities |
| **Custom checkbox** | `run_directory_checkbox()` | A prompt_toolkit `Application` that renders an interactive checkbox list with toggle, select-all, preview, and escape support |
| **ExclusionWizard** | `ExclusionWizard` class | Stateful wizard that recursively walks subdirectories, letting the user skip/keep/preview at each level with full undo support |
| **Main flow** | `if __name__ == '__main__'` | Sequential prompts for source → destination → exclusion → confirmation → copy |

## License

[MIT](LICENSE)
