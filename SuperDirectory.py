# requires: pip install questionary
import os
import shutil
import questionary

script_name = os.path.basename(__file__)


def get_subdirs(directory):
    """Return sorted list of immediate subdirectory names."""
    try:
        return sorted([d for d in os.listdir(directory)
                       if os.path.isdir(os.path.join(directory, d))])
    except PermissionError:
        return []


class ExclusionWizard:
    """
    Walks the directory tree one level at a time, letting the user pick
    which folders to skip. Supports going back, starting over, and
    skipping the rest of the exclusion process at any point.
    """

    def __init__(self, source):
        self.source = source
        self.excluded = []

    def run(self):
        while True:
            result = self._process(self.source)
            if result == 'start_over':
                print("\n  Starting over...\n")
                self.excluded = []
                continue
            break
        return self.excluded

    def _process(self, directory):
        subdirs = get_subdirs(directory)
        if not subdirs:
            return 'done'

        while True:
            dir_label = os.path.basename(directory) or directory
            is_root = (directory == self.source)

            choices = [
                questionary.Choice("Select folders to skip", value='select'),
                questionary.Choice("Skip ALL subfolders here", value='skip_all'),
                questionary.Choice("Keep ALL subfolders (move on)", value='keep_all'),
                questionary.Separator(),
                questionary.Choice("⏩  Skip remaining exclusion checks", value='skip_remaining'),
            ]
            if not is_root:
                choices += [
                    questionary.Choice("↩   Go back one level", value='go_back'),
                    questionary.Choice("↩↩  Start over", value='start_over'),
                ]

            action = questionary.select(
                f"Subfolders in [{dir_label}]:", choices=choices
            ).ask()
            if action is None:
                exit()

            if action == 'select':
                to_skip = questionary.checkbox(
                    "Select folders to SKIP  (space to toggle, enter to confirm):",
                    choices=subdirs
                ).ask()
                if to_skip is None:
                    to_skip = []

                snapshot = list(self.excluded)
                for d in to_skip:
                    self.excluded.append(os.path.join(directory, d))

                kept = [d for d in subdirs if d not in to_skip]
                result = self._process_children(directory, kept)

                if result == 'go_back':
                    self.excluded[:] = snapshot  # undo this level's choices
                    continue                      # re-ask this level
                elif result == 'start_over':
                    self.excluded[:] = snapshot
                    return 'start_over'
                return result

            elif action == 'skip_all':
                for d in subdirs:
                    self.excluded.append(os.path.join(directory, d))
                return 'done'

            elif action == 'keep_all':
                return 'done'

            elif action == 'skip_remaining':
                return 'skip_remaining'

            elif action == 'go_back':
                return 'go_back'

            elif action == 'start_over':
                return 'start_over'

    def _process_children(self, parent, kept_subdirs):
        for d in kept_subdirs:
            result = self._process(os.path.join(parent, d))
            if result in ('go_back', 'start_over', 'skip_remaining'):
                return result
        return 'done'


# ── Source directory ──────────────────────────────────────────────────────────
print(f"\nCurrent directory: {os.getcwd()}")

use_current = questionary.confirm(
    "Are you already in the folder you want to use as the source?"
).ask()

if use_current:
    source_directory = os.getcwd()
else:
    while True:
        raw = questionary.text("Enter the path to the source folder:").ask()
        if raw is None:
            exit()
        path = os.path.expanduser(raw.strip())
        if os.path.isdir(path):
            source_directory = path
            break
        print(f"  Couldn't find '{raw.strip()}'. Check for typos and try again.")

# ── Destination ───────────────────────────────────────────────────────────────
print()
while True:
    raw = questionary.text("Where do you want your SuperDirectory saved? (e.g. ~/Desktop):").ask()
    if raw is None:
        exit()
    base_path = os.path.expanduser(raw.strip())
    if os.path.isdir(base_path):
        break
    print(f"  Couldn't find '{raw.strip()}'. Check for typos and try again.")

folder_name = questionary.text("What do you want to name your SuperDirectory?").ask()
if folder_name is None:
    exit()
folder_name = folder_name.strip()
target_directory = os.path.join(base_path, folder_name)

# ── Subfolders / exclusion ────────────────────────────────────────────────────
immediate = get_subdirs(source_directory)
excluded_paths = []

if not immediate:
    print("\nNo subfolders detected — you're already in a SuperDirectory!")
    if not questionary.confirm("Copy all files to a new directory anyway?").ask():
        print("Exiting.")
        exit()
else:
    if questionary.confirm("Would you like to exclude any subfolders?").ask():
        excluded_paths = ExclusionWizard(source_directory).run()

# ── Confirmation ──────────────────────────────────────────────────────────────
source_name = os.path.basename(source_directory) or source_directory
print("\n--- Ready to copy ---")
print(f"  From: {source_directory}")
print(f"  To:   {target_directory}")
if excluded_paths:
    print(f"  Skipping {len(excluded_paths)} folder(s):")
    for p in excluded_paths:
        print(f"    - {os.path.relpath(p, source_directory)}")
else:
    print("  No folders will be skipped.")

if not questionary.confirm(
    f"[{source_name}] files will be copied to [{folder_name}] at [{target_directory}]. Continue?"
).ask():
    print("Cancelled.")
    exit()

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
total = len(files_to_copy)
if total == 0:
    print("\n  No files found to copy.")
else:
    print(f"\n  Copying {total} file(s)...\n")
    bar_width = 40
    for i, (src, dst) in enumerate(files_to_copy):
        shutil.copy(src, dst)
        done = i + 1
        filled = int(bar_width * done / total)
        bar = '█' * filled + '░' * (bar_width - filled)
        print(f"\r  [{bar}] {int(done / total * 100)}%  ({done}/{total})", end='', flush=True)
    print()

print("\nFinished!")
