import os
import sys
import shutil
import questionary
from questionary import Style
from prompt_toolkit import Application
from prompt_toolkit.layout import Layout
from prompt_toolkit.layout.containers import Window
from prompt_toolkit.layout.controls import FormattedTextControl
from prompt_toolkit.key_binding import KeyBindings
from prompt_toolkit.styles import Style as PTKStyle
from prompt_toolkit.shortcuts import CompleteStyle

script_name = os.path.basename(__file__)

# ── ANSI colour helpers (print() only — never inside questionary messages) ────
def green(t):  return f"\033[32m{t}\033[0m"
def red(t):    return f"\033[31m{t}\033[0m"
def cyan(t):   return f"\033[36m{t}\033[0m"
def bold(t):   return f"\033[1m{t}\033[0m"
def dim(t):    return f"\033[2m{t}\033[0m"

def format_path_red_tail(path):
    """Color only the last component of a path red: /some/path/LAST_IN_RED"""
    path = path.rstrip(os.sep)
    head, tail = os.path.split(path)
    return os.path.join(head, red(tail)) if head else red(tail)

# ── Questionary style ─────────────────────────────────────────────────────────
STYLE = Style([
    ('qmark',                        'fg:#00b4d8 bold'),
    ('question',                     'fg:#ffffff bold'),
    ('answer',                       'fg:#00b4d8 bold'),
    ('pointer',                      'fg:#00b4d8 bold'),
    ('highlighted',                  'fg:#00b4d8 bold'),
    ('checkbox-selected',            'fg:#e74c3c bold'),
    ('selected',                     'fg:#e74c3c bold'),
    ('checkbox',                     'fg:#2ecc71'),
    ('separator',                    'fg:#444444'),
    ('instruction',                  'fg:#888888 italic'),
    ('highlighted-checkbox-selected','fg:#e74c3c bold'),
])

# Prompt_toolkit style for the custom checkbox widget
CHECKBOX_STYLE = PTKStyle.from_dict({
    'question':    '#ffffff bold',
    'instruction': '#888888',
    'highlighted': '#00b4d8 bold',
    'selected':    '#e74c3c bold',
    'pointer':     '#00b4d8 bold',
    'text':        '#aaaaaa',
})

# ── Helpers ───────────────────────────────────────────────────────────────────
def ask(prompt_obj, on_cancel='exit'):
    """
    on_cancel='exit'  → Ctrl+C or Esc exits the program (default)
    on_cancel='back'  → returns None so the caller can handle it
    """
    try:
        result = prompt_obj.ask()
    except KeyboardInterrupt:
        result = None
    if result is None:
        if on_cancel == 'exit':
            print("\n\n  Exiting.")
            sys.exit(0)
        return None
    return result


def yn_select(message):
    """Yes/No selector with arrow-key navigation and y/n keyboard shortcuts."""
    return ask(questionary.select(
        message,
        choices=[
            questionary.Choice("Yes", shortcut_key='y', value=True),
            questionary.Choice("No",  shortcut_key='n', value=False),
        ],
        style=STYLE,
    ))


def section(title):
    pad = "─" * max(0, 48 - len(title))
    print(f"\n{dim(f'  ── {title} {pad}')}\n")


def get_subdirs(directory):
    try:
        return sorted([d for d in os.listdir(directory)
                       if os.path.isdir(os.path.join(directory, d))])
    except PermissionError:
        return []


# ── Custom directory checkbox ─────────────────────────────────────────────────
# Built with prompt_toolkit directly so we can support:
#   space   = toggle skip/keep
#   a       = select / deselect all
#   enter   = confirm
#   p       = preview highlighted directory (if preview_fn provided)
#   Esc     = go back to menu (returns None)
#   Ctrl+C  = exit program
def run_directory_checkbox(message, choices, pre_checked=None, preview_fn=None):
    """
    preview_fn: callable(dir_name) → 'skip' | 'keep' | 'back'
    Returns list of selected (to-skip) directory names, or None on Esc.
    """
    selected = set(pre_checked or [])
    cursor   = [0]
    action   = ['']
    n        = len(choices)

    def get_text():
        tokens = [('class:question', f'  ? {message}\n')]
        instr = '  (↑↓ = navigate · space = toggle · a = select all · enter = confirm'
        if preview_fn:
            instr += ' · p = preview highlighted'
        instr += ' · Esc = back to menu)\n\n'
        tokens.append(('class:instruction', instr))
        for i, choice in enumerate(choices):
            is_checked = choice in selected
            is_focused = i == cursor[0]
            marker     = '◉ ' if is_checked else '◯ '
            style      = 'class:selected' if is_checked else 'class:text'
            tokens.append(('class:pointer', '  ❯ ') if is_focused else ('', '    '))
            tokens.append((style, f'{marker}{choice}\n'))
        return tokens

    kb = KeyBindings()

    @kb.add('up')
    def _(e): cursor[0] = max(0, cursor[0] - 1)

    @kb.add('down')
    def _(e): cursor[0] = min(n - 1, cursor[0] + 1)

    @kb.add('space')
    def _(e):
        item = choices[cursor[0]]
        selected.discard(item) if item in selected else selected.add(item)

    @kb.add('a')
    def _(e):
        if selected == set(choices):
            selected.clear()
        else:
            selected.update(choices)

    @kb.add('enter')
    def _(e):
        action[0] = 'confirm'
        e.app.exit()

    @kb.add('escape')
    def _(e):
        action[0] = 'back'
        e.app.exit()

    @kb.add('c-c')
    def _(e):
        action[0] = 'exit'
        e.app.exit()

    if preview_fn:
        @kb.add('p')
        def _(e):
            action[0] = 'preview'
            e.app.exit()

    while True:
        action[0] = ''
        layout = Layout(Window(
            content=FormattedTextControl(get_text, focusable=True),
            wrap_lines=True,
        ))
        Application(layout=layout, key_bindings=kb, style=CHECKBOX_STYLE,
                    full_screen=False, mouse_support=False).run()

        if action[0] == 'exit':
            print('\n\n  Exiting.')
            sys.exit(0)
        elif action[0] == 'back':
            return None
        elif action[0] == 'confirm':
            return [c for c in choices if c in selected]
        elif action[0] == 'preview' and preview_fn:
            print()
            result = preview_fn(choices[cursor[0]])
            if result == 'skip':
                selected.add(choices[cursor[0]])
            elif result == 'keep':
                selected.discard(choices[cursor[0]])
            # loop — checkbox redraws with updated state


# ── ExclusionWizard ───────────────────────────────────────────────────────────
class ExclusionWizard:

    def __init__(self, source, initial_excluded=None):
        self.source   = source
        self.excluded = set(initial_excluded or [])

    def run(self):
        while True:
            result = self._process(self.source)
            if result == 'start_over':
                print(f"\n  {cyan('Starting over...')}\n")
                self.excluded.clear()
                continue
            break
        return sorted(self.excluded)

    def _process(self, directory):
        subdirs = get_subdirs(directory)
        if not subdirs:
            return 'done'

        while True:
            dir_label = os.path.basename(directory) or directory
            is_root   = (directory == self.source)

            print()
            choices = [
                questionary.Choice("👆  Select directories to skip",      shortcut_key='s', value='select'),
                questionary.Choice("⏩  Skip ALL directories here",        shortcut_key='a', value='skip_all'),
                questionary.Choice("✅  Keep ALL directories (continue)",  shortcut_key='k', value='keep_all'),
                questionary.Separator(),
                questionary.Choice("⏭️  Skip remaining exclusion checks",  shortcut_key='f', value='skip_remaining'),
                questionary.Choice("🔍  Preview a directory",              shortcut_key='p', value='preview'),
            ]
            if not is_root:
                choices += [
                    questionary.Choice("⏪  Go back one level",  shortcut_key='b', value='go_back'),
                    questionary.Choice("🔄  Start over",          shortcut_key='r', value='start_over'),
                ]

            action = ask(questionary.select(
                f"Subdirectories in [{dir_label}]:",
                choices=choices,
                style=STYLE,
            ))

            if action == 'preview':
                self._pick_and_preview(directory, subdirs)
                continue

            if action == 'select':
                pre_checked = [d for d in subdirs
                               if os.path.join(directory, d) in self.excluded]
                preview_fn  = lambda d: self._show_preview(os.path.join(directory, d))

                to_skip = run_directory_checkbox(
                    "Select directories to SKIP  (red = will be skipped)",
                    choices=subdirs,
                    pre_checked=pre_checked,
                    preview_fn=preview_fn,
                )

                if to_skip is None:
                    continue  # Esc → back to menu

                snapshot = set(self.excluded)
                self.excluded -= {os.path.join(directory, d) for d in subdirs}
                self.excluded |= {os.path.join(directory, d) for d in to_skip}

                kept   = [d for d in subdirs if os.path.join(directory, d) not in self.excluded]
                result = self._process_children(directory, kept)

                if result == 'go_back':
                    self.excluded = snapshot
                    continue
                elif result == 'start_over':
                    self.excluded = snapshot
                    return 'start_over'
                return result

            elif action == 'skip_all':
                self.excluded |= {os.path.join(directory, d) for d in subdirs}
                return 'done'

            elif action == 'keep_all':
                return 'done'

            elif action == 'skip_remaining':
                return 'skip_remaining'

            elif action == 'go_back':
                return 'go_back'

            elif action == 'start_over':
                return 'start_over'

    def _pick_and_preview(self, parent, subdirs):
        """Main-menu preview: ask which directory to inspect, then show it."""
        choices = [questionary.Choice(f"📁  {d}", value=d) for d in subdirs]
        choices.append(questionary.Separator())
        choices.append(questionary.Choice("⏪  Back", value='back'))

        pick = ask(questionary.select(
            "Which directory would you like to preview?",
            choices=choices,
            style=STYLE,
        ))
        if pick == 'back':
            return

        print()
        fp     = os.path.join(parent, pick)
        result = self._show_preview(fp)
        if result == 'skip':
            self.excluded.add(fp)
            print(f"\n  {red('✗')} [{pick}] marked for exclusion.\n")
        elif result == 'keep':
            self.excluded.discard(fp)
            print(f"\n  {green('✓')} [{pick}] will be included.\n")

    def _show_preview(self, full_path):
        """
        Show directory contents and ask what to do.
        Returns 'skip', 'keep', or 'back'.
        Called from both the menu preview and the checkbox p-key preview.
        """
        dir_name    = os.path.basename(full_path)
        sub_subdirs = get_subdirs(full_path)
        try:
            files = sorted([f for f in os.listdir(full_path)
                            if os.path.isfile(os.path.join(full_path, f))])
        except PermissionError:
            files = []

        print(f"\n  {bold('Contents of')} [{cyan(dir_name)}]:")
        if sub_subdirs:
            print(f"\n  Subdirectories ({len(sub_subdirs)}):")
            for d in sub_subdirs:
                print(f"    📁  {d}")
        if files:
            print(f"\n  Files ({len(files)}):")
            for f in files[:15]:
                print(f"    📄  {f}")
            if len(files) > 15:
                print(f"    {dim(f'… and {len(files) - 15} more')}")
        if not sub_subdirs and not files:
            print(f"  {dim('(empty)')}")
        print()

        return ask(questionary.select(
            f"What would you like to do with [{dir_name}]?",
            choices=[
                questionary.Choice("●  Skip this directory (exclude it)",  value='skip'),
                questionary.Choice("●  Keep this directory (include it)",   value='keep'),
                questionary.Separator(),
                questionary.Choice("⏪  Back to selection",                  value='back'),
            ],
            style=STYLE,
        ))

    def _process_children(self, parent, kept_subdirs):
        for d in kept_subdirs:
            result = self._process(os.path.join(parent, d))
            if result in ('go_back', 'start_over', 'skip_remaining'):
                return result
        return 'done'


# ── Source directory ──────────────────────────────────────────────────────────
print(f"\n  {dim('Ctrl+C exits at any time.')}")
print(f"  {dim('In checkboxes: space = toggle · a = select all · enter = confirm · Esc = back')}\n")
section("Source Directory")

print(f"  Current directory: {cyan(os.getcwd())}\n")

use_current = yn_select("Are you already in the directory you want to use as the source?")

if use_current:
    source_directory = os.getcwd()
else:
    print(f"\n  {dim('Tip: Tab autocompletes paths. Use ~/ to start from your home directory.')}\n")
    while True:
        raw = ask(questionary.path(
            "Enter the path to the source directory:",
            style=STYLE,
            only_directories=True,
            complete_style=CompleteStyle.COLUMN,
        ))
        path = os.path.expanduser(raw.strip())
        if os.path.isdir(path):
            source_directory = path
            break
        print(f"\n  {red('✗')} Couldn't find '{raw.strip()}'. Check for typos and try again.\n")

source_name = os.path.basename(source_directory) or source_directory

# ── Destination ───────────────────────────────────────────────────────────────
section("Destination")
print(f"  {dim('Tip: Tab autocompletes paths. Use ~/ to start from your home directory.')}\n")

while True:
    raw = ask(questionary.path(
        "Where do you want your SuperDirectory saved?",
        style=STYLE,
        only_directories=True,
        complete_style=CompleteStyle.COLUMN,
    ))
    base_path = os.path.expanduser(raw.strip())
    if os.path.isdir(base_path):
        break
    print(f"\n  {red('✗')} Couldn't find '{raw.strip()}'. Check for typos and try again.\n")

dir_name = ask(questionary.text(
    "What do you want to name your SuperDirectory?",
    style=STYLE,
)).strip()
target_directory = os.path.join(base_path, dir_name)

# ── Exclusion ─────────────────────────────────────────────────────────────────
section("Exclusion")

immediate      = get_subdirs(source_directory)
excluded_paths = []

if not immediate:
    print(f"  {cyan('No subdirectories detected')} — you're already in a SuperDirectory!\n")
    if not yn_select("Copy all files to a new directory anyway?"):
        print("\n  Exiting.")
        sys.exit(0)
else:
    if yn_select(f"Would you like to {red('exclude')} any subdirectories?"):
        excluded_paths = ExclusionWizard(source_directory).run()


# ── Confirmation ──────────────────────────────────────────────────────────────
def show_summary():
    section("Confirmation")
    print(f"  From:  {format_path_red_tail(source_directory)}")
    print(f"  To:    {format_path_red_tail(target_directory)}")
    n = len(excluded_paths)
    if n == 0:
        print(f"\n  {green('✓')} No directories will be excluded.")
    elif n == 1:
        print(f"\n  Excluding 1 directory:")
        print(f"    {red('✗')}  {os.path.relpath(excluded_paths[0], source_directory)}")
    else:
        print(f"\n  Excluding {n} directories:")
        for p in excluded_paths:
            print(f"    {red('✗')}  {os.path.relpath(p, source_directory)}")
    print()


show_summary()

while True:
    confirmed = yn_select(
        f"[{source_name}] files will be copied to [{dir_name}] at [{target_directory}]. Continue?"
    )

    if confirmed:
        break

    action = ask(questionary.select(
        "What would you like to do?",
        choices=[
            questionary.Choice("▶️  Continue anyway",                    value='continue'),
            questionary.Choice("⏪  Go back to exclusion settings",      value='exclusion'),
            questionary.Choice("🔄  Start over from the beginning",      value='restart'),
            questionary.Separator(),
            questionary.Choice("✗   Quit",                               value='quit'),
        ],
        style=STYLE,
    ))

    if action == 'continue':
        break
    elif action == 'quit':
        print("\n  Exiting.")
        sys.exit(0)
    elif action == 'restart':
        print(f"\n  {cyan('Restarting...')}\n")
        os.execv(sys.executable, [sys.executable] + sys.argv)
    elif action == 'exclusion':
        section("Exclusion")
        if excluded_paths:
            keep   = yn_select("Keep your previous exclusion settings as a starting point?")
            wizard = ExclusionWizard(source_directory,
                                     initial_excluded=excluded_paths if keep else None)
        else:
            wizard = ExclusionWizard(source_directory)
        excluded_paths = wizard.run()
        show_summary()


# ── Build file list ───────────────────────────────────────────────────────────
os.makedirs(target_directory, exist_ok=True)
excluded_set   = set(excluded_paths)
files_to_copy  = []

for root, dirs, files in os.walk(source_directory):
    dirs[:] = [d for d in dirs if os.path.join(root, d) not in excluded_set]

    for name in files:
        if name == script_name:
            continue

        source_path      = os.path.join(root, name)
        new_name         = name if root == source_directory else f"{os.path.basename(root)}_{name}"
        destination_path = os.path.join(target_directory, new_name)

        if os.path.exists(destination_path):
            base, ext = os.path.splitext(new_name)
            counter = 1
            while os.path.exists(destination_path):
                destination_path = os.path.join(target_directory, f"{base}_{counter}{ext}")
                counter += 1

        files_to_copy.append((source_path, destination_path))

# ── Copy with progress bar ────────────────────────────────────────────────────
section("Copying")
total = len(files_to_copy)

if total == 0:
    print(f"  {dim('No files found to copy.')}")
else:
    print(f"  Copying {bold(str(total))} file(s)...\n")
    bar_width = 40
    for i, (src, dst) in enumerate(files_to_copy):
        shutil.copy(src, dst)
        done   = i + 1
        filled = int(bar_width * done / total)
        bar    = green('█' * filled) + dim('░' * (bar_width - filled))
        print(f"\r  [{bar}] {int(done / total * 100):3}%  ({done}/{total})", end='', flush=True)
    print()

print(f"\n  {bold(green('Finished!'))}\n")
