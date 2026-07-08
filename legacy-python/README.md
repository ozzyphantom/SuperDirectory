# SuperDirectory — the original Python script

**Superseded.** The app lives at the repository root and is written in Go. This directory keeps the original single-file Python implementation as a behavior and UX reference. It is not maintained, and it does not have the organize-by-file-type mode.

See [`../roadmap.md`](../roadmap.md) for why the rewrite happened. Short version: the copy is I/O-bound, so Go bought no speed. It bought a zero-runtime single binary that works on native Windows, and real parallelism for the CPU-bound features on the roadmap. `start.sh` is bash, so it never ran on Windows to begin with.

## Running it

Requires Python 3.8+. The launcher creates a virtualenv and installs dependencies:

```bash
cd legacy-python
./start.sh
```

Or manually:

```bash
python -m venv venv
source venv/bin/activate       # On Windows: venv\Scripts\activate
pip install -r requirements.txt
python SuperDirectory.py
```

## Dependencies

- [questionary](https://github.com/tmbo/questionary) — terminal UI prompts
- [prompt_toolkit](https://github.com/prompt-toolkit/python-prompt-toolkit) — custom checkbox widget and path completion

## Code organization (`SuperDirectory.py`)

| Section | Purpose |
|---|---|
| **Imports** | Standard library + questionary + prompt_toolkit |
| **ANSI helpers** | `green()`, `red()`, `cyan()`, `orange()`, `bold()`, `dim()` — terminal color formatting |
| **Styles** | `STYLE`, `CHECKBOX_STYLE` — theme definitions for questionary and prompt_toolkit |
| **Helpers** | `ask()`, `yn_select()`, `section()`, `get_subdirs()`, `ask_directory()`, `ask_dirname()` |
| **Custom checkbox** | `run_directory_checkbox()` — a prompt_toolkit `Application` rendering an interactive checkbox list with toggle, select-all, preview, and escape |
| **ExclusionWizard** | Stateful wizard that recursively walks subdirectories, letting the user skip/keep/preview at each level with full undo |
| **Main flow** | Sequential prompts: source → destination → exclusion → confirmation → copy |

## Behavior differences from the Go app

- **No organize-by-file-type mode.** Flatten only.
- **Text-entry paths with tab completion**, rather than the Go app's keyboard directory browser.
- **No copy-into-itself guard.** The Go app refuses a destination inside the source; this script will happily copy a tree into itself.
- **Select-all (`a`) in the exclusion checkbox.** The Go exclusion tree has no equivalent yet.
