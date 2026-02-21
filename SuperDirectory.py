# requires: pip install questionary
import os
import sys
import shutil
import questionary
from questionary import Style

script_name = os.path.basename(__file__)

# ── ANSI colour helpers ───────────────────────────────────────────────────────
# IMPORTANT: Only use these in print() calls.
# Never pass them inside questionary message strings — questionary uses its own
# style system and will render ANSI escape codes as literal characters.
def green(t):  return f"\033[32m{t}\033[0m"
def red(t):    return f"\033[31m{t}\033[0m"
def cyan(t):   return f"\033[36m{t}\033[0m"
def bold(t):   return f"\033[1m{t}\033[0m"
def dim(t):    return f"\033[2m{t}\033[0m"

# ── Questionary style ─────────────────────────────────────────────────────────
STYLE = Style([
    ('qmark',                      'fg:#00b4d8 bold'),
    ('question',                   'fg:#ffffff bold'),
    ('answer',                     'fg:#00b4d8 bold'),
    ('pointer',                    'fg:#00b4d8 bold'),
    ('highlighted',                'fg:#00b4d8 bold'),
    ('checkbox-selected',          'fg:#e74c3c bold'),  # red ◉ bubble = will be skipped
    ('selected',                   'fg:#e74c3c bold'),  # red text     = will be skipped
    ('checkbox',                   'fg:#2ecc71'),       # green ◯ bubble = will be kept
    ('separator',                  'fg:#444444'),
    ('instruction',                'fg:#888888 italic'),
    ('highlighted-checkbox-selected', 'fg:#e74c3c bold'),
])

# ── Helpers ───────────────────────────────────────────────────────────────────
def ask(prompt_obj, on_cancel='exit'):
    """
    Wraps any questionary prompt.
    on_cancel='exit'  → Ctrl+C or Esc exits the program (default)
    on_cancel='back'  → returns None so the caller can handle it (e.g. Esc in checkbox)
    """
    try:
        result = prompt_obj.ask()
    except KeyboardInterrupt:
        result = None
    if result is None:
        if on_cancel == 'exit':
            print("\n\n  Exiting.")
            sys.exit(0)
        return None   # on_cancel='back'
    return result


def section(title):
    pad = "─" * max(0, 48 - len(title))
    print(f"\n{dim(f'  ── {title} {pad}')}\n")


def get_subdirs(directory):
    try:
        return sorted([d for d in os.listdir(directory)
                       if os.path.isdir(os.path.join(directory, d))])
    except PermissionError:
        return []


# ── ExclusionWizard ───────────────────────────────────────────────────────────
class ExclusionWizard:

    def __init__(self, source):
        self.source = source
        self.excluded = []

    def run(self):
        while True:
            result = self._process(self.source)
            if result == 'start_over':
                print(f"\n  {cyan('Starting over...')}\n")
                self.excluded = []
                continue
            break
        return self.excluded

    def _process(self, directory):
        subdirs = get_subdirs(directory)
        if not subdirs:
            return 'done'

        while True:
            # Plain text only in questionary messages — no ANSI helpers here
            dir_label = os.path.basename(directory) or directory
            is_root = (directory == self.source)

            print()
            choices = [
                questionary.Choice("👆  Select directories to skip",       shortcut_key='s', value='select'),
                questionary.Choice("⏩  Skip ALL directories here",         shortcut_key='a', value='skip_all'),
                questionary.Choice("✅  Keep ALL directories (continue)",   shortcut_key='k', value='keep_all'),
                questionary.Separator(),
                questionary.Choice("⏭️   Skip remaining exclusion checks",   shortcut_key='f', value='skip_remaining'),
                questionary.Choice("🔍  Preview a directory",               shortcut_key='p', value='preview'),
            ]
            if not is_root:
                choices += [
                    questionary.Choice("⏪  Go back one level",  shortcut_key='b', value='go_back'),
                    questionary.Choice("🔄  Start over",          shortcut_key='r', value='start_over'),
                ]

            action = ask(questionary.select(
                f"Subdirectories in [{dir_label}]:",   # plain text — no cyan()
                choices=choices,
                style=STYLE,
            ))

            if action == 'preview':
                self._preview(directory, subdirs)
                continue

            if action == 'select':
                preview_excluded = [d for d in subdirs
                                    if os.path.join(directory, d) in self.excluded]

                checkbox_choices = [
                    questionary.Choice(title=d, value=d, checked=(d in preview_excluded))
                    for d in subdirs
                ]

                # Esc during checkbox → back to this menu (not exit)
                to_skip = ask(
                    questionary.checkbox(
                        "Select directories to SKIP  (red = skipped)",
                        choices=checkbox_choices,
                        style=STYLE,
                        instruction="(↑↓ navigate · space toggle · enter confirm · Esc back to menu)",
                    ),
                    on_cancel='back',
                )

                if to_skip is None:
                    continue  # user pressed Esc — redisplay the menu

                snapshot = list(self.excluded)

                # Clear preview flags for this level, then apply checkbox result
                for d in subdirs:
                    fp = os.path.join(directory, d)
                    if fp in self.excluded:
                        self.excluded.remove(fp)
                for d in to_skip:
                    self.excluded.append(os.path.join(directory, d))

                kept = [d for d in subdirs if os.path.join(directory, d) not in self.excluded]
                result = self._process_children(directory, kept)

                if result == 'go_back':
                    self.excluded[:] = snapshot
                    continue
                elif result == 'start_over':
                    self.excluded[:] = snapshot
                    return 'start_over'
                return result

            elif action == 'skip_all':
                for d in subdirs:
                    fp = os.path.join(directory, d)
                    if fp not in self.excluded:
                        self.excluded.append(fp)
                return 'done'

            elif action == 'keep_all':
                return 'done'

            elif action == 'skip_remaining':
                return 'skip_remaining'

            elif action == 'go_back':
                return 'go_back'

            elif action == 'start_over':
                return 'start_over'

    def _preview(self, parent, subdirs):
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

        full_path = os.path.join(parent, pick)
        sub_subdirs = get_subdirs(full_path)
        try:
            files = sorted([f for f in os.listdir(full_path)
                            if os.path.isfile(os.path.join(full_path, f))])
        except PermissionError:
            files = []

        # ANSI in print() is fine
        print(f"\n  {bold('Contents of')} [{cyan(pick)}]:")
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

        action = ask(questionary.select(
            f"What would you like to do with [{pick}]?",  # plain text
            choices=[
                questionary.Choice("●  Skip this directory (exclude it)",  value='skip'),
                questionary.Choice("●  Keep this directory (include it)",   value='keep'),
                questionary.Separator(),
                questionary.Choice("⏪  Back to directory selection",        value='back'),
            ],
            style=STYLE,
        ))

        if action == 'skip':
            if full_path not in self.excluded:
                self.excluded.append(full_path)
            print(f"\n  {red('✗')} [{pick}] marked for exclusion.\n")
        elif action == 'keep':
            if full_path in self.excluded:
                self.excluded.remove(full_path)
            print(f"\n  {green('✓')} [{pick}] will be included.\n")

    def _process_children(self, parent, kept_subdirs):
        for d in kept_subdirs:
            result = self._process(os.path.join(parent, d))
            if result in ('go_back', 'start_over', 'skip_remaining'):
                return result
        return 'done'


# ── Source directory ──────────────────────────────────────────────────────────
print(f"\n  {dim('Ctrl+C exits at any time.')}")
print(f"  {dim('In checkboxes: space = toggle selection · enter = confirm · Esc = back to menu')}\n")
section("Source Directory")

print(f"  Current directory: {cyan(os.getcwd())}\n")

use_current = ask(questionary.confirm(
    "Are you already in the directory you want to use as the source?",
    style=STYLE,
))

if use_current:
    source_directory = os.getcwd()
else:
    print(f"\n  {dim('Tip: Press Tab to autocomplete. Start with ~ for your home directory.')}\n")
    while True:
        raw = ask(questionary.path(
            "Enter the path to the source directory:",
            style=STYLE,
            only_directories=True,
        ))
        path = os.path.expanduser(raw.strip())
        if os.path.isdir(path):
            source_directory = path
            break
        print(f"\n  {red('✗')} Couldn't find '{raw.strip()}'. Check for typos and try again.\n")

source_name = os.path.basename(source_directory) or source_directory

# ── Destination ───────────────────────────────────────────────────────────────
section("Destination")
print(f"  {dim('Tip: Press Tab to autocomplete. Start with ~ for your home directory.')}\n")

while True:
    raw = ask(questionary.path(
        "Where do you want your SuperDirectory saved?",
        style=STYLE,
        only_directories=True,
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

immediate = get_subdirs(source_directory)
excluded_paths = []

if not immediate:
    print(f"  {cyan('No subdirectories detected')} — you're already in a SuperDirectory!\n")
    if not ask(questionary.confirm(
        "Copy all files to a new directory anyway?",
        style=STYLE,
    )):
        print("\n  Exiting.")
        sys.exit(0)
else:
    if ask(questionary.confirm(
        "Would you like to exclude any subdirectories?",
        style=STYLE,
    )):
        excluded_paths = ExclusionWizard(source_directory).run()


# ── Confirmation ──────────────────────────────────────────────────────────────
def show_summary():
    section("Confirmation")
    print(f"  From:  {cyan(source_directory)}")
    print(f"  To:    {cyan(target_directory)}")
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
    confirmed = ask(questionary.confirm(
        f"[{source_name}] files will be copied to [{dir_name}] at [{target_directory}]. Continue?",
        style=STYLE,
    ))

    if confirmed:
        break

    action = ask(questionary.select(
        "What would you like to do?",
        choices=[
            questionary.Choice("⏪  Go back to exclusion settings",  value='exclusion'),
            questionary.Choice("🔄  Start over from the beginning",  value='restart'),
            questionary.Separator(),
            questionary.Choice("✗   Quit",                           value='quit'),
        ],
        style=STYLE,
    ))

    if action == 'quit':
        print("\n  Exiting.")
        sys.exit(0)
    elif action == 'restart':
        print(f"\n  {cyan('Restarting...')}\n")
        os.execv(sys.executable, [sys.executable] + sys.argv)
    elif action == 'exclusion':
        section("Exclusion")
        excluded_paths = ExclusionWizard(source_directory).run()
        show_summary()


# ── Build file list ───────────────────────────────────────────────────────────
os.makedirs(target_directory, exist_ok=True)
files_to_copy = []

for root, dirs, files in os.walk(source_directory):
    dirs[:] = [d for d in dirs if os.path.join(root, d) not in excluded_paths]

    for name in files:
        if name == script_name:
            continue

        source_path = os.path.join(root, name)
        new_name = name if root == source_directory else f"{os.path.basename(root)}_{name}"
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
        done = i + 1
        filled = int(bar_width * done / total)
        bar = green('█' * filled) + dim('░' * (bar_width - filled))
        print(f"\r  [{bar}] {int(done / total * 100):3}%  ({done}/{total})", end='', flush=True)
    print()

print(f"\n  {bold(green('Finished!'))}\n")
