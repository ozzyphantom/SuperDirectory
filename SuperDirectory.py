import os
import shutil

script_name = os.path.basename(__file__)

# --- Source directory ---
print(f"\nCurrent directory: {os.getcwd()}")

while True:
    choice = input("Are you already in the folder you want to use? (y/n): ").strip().lower()
    if choice == 'y':
        source_directory = os.getcwd()
        break
    elif choice == 'n':
        while True:
            raw = input("Enter the path to the source folder: ").strip()
            path = os.path.expanduser(raw)
            if os.path.isdir(path):
                source_directory = path
                break
            else:
                print(f"  Couldn't find '{raw}'. Check for typos and try again.")
        break
    else:
        print("  Please enter 'y' or 'n'.")

# --- Destination ---
print()
while True:
    raw = input("Where do you want your SuperDirectory saved? (e.g. ~/Desktop): ").strip()
    base_path = os.path.expanduser(raw)
    if os.path.isdir(base_path):
        break
    else:
        print(f"  Couldn't find '{raw}'. Check for typos and try again.")

folder_name = input("What do you want to name your SuperDirectory? (e.g. My Merged Files): ").strip()
target_directory = os.path.join(base_path, folder_name)


# --- Helper to list subdirectories ---
def get_subdirs(directory):
    try:
        return sorted([d for d in os.listdir(directory)
                       if os.path.isdir(os.path.join(directory, d))])
    except PermissionError:
        return []


# --- Check for subfolders ---
immediate = get_subdirs(source_directory)
excluded_paths = []

if not immediate:
    print("\nNo subfolders detected — you're already in a SuperDirectory!")
    while True:
        choice = input("Copy all files to a new directory anyway? (y/n): ").strip().lower()
        if choice == 'y':
            break
        elif choice == 'n':
            print("Exiting.")
            exit()
        else:
            print("  Please enter 'y' or 'n'.")
else:
    # --- Skip folders, one level at a time ---
    def ask_skip(directory):
        subdirs = get_subdirs(directory)
        if not subdirs:
            return

        dir_label = os.path.basename(directory) or directory
        print(f"\nSubfolders in [{dir_label}]:")
        for i, d in enumerate(subdirs):
            print(f"  {i}: {d}")
        print("  (Press Enter to keep all)")

        while True:
            choices = input("Which folders would you like to skip? ").strip()
            if not choices:
                skipped = set()
                break
            try:
                indices = [int(x.strip()) for x in choices.split(",")]
                if all(0 <= i < len(subdirs) for i in indices):
                    skipped = {subdirs[i] for i in indices}
                    break
                else:
                    print(f"  Please use numbers between 0 and {len(subdirs) - 1}.")
            except ValueError:
                print("  Invalid input. Enter numbers separated by commas.")

        for d in subdirs:
            full_path = os.path.join(directory, d)
            if d in skipped:
                excluded_paths.append(full_path)
            else:
                ask_skip(full_path)

    ask_skip(source_directory)


# --- Confirmation ---
source_name = os.path.basename(source_directory) or source_directory
print(f"\n--- Ready to copy ---")
print(f"  From: {source_directory}")
print(f"  To:   {target_directory}")
if excluded_paths:
    print(f"  Skipping {len(excluded_paths)} folder(s):")
    for p in excluded_paths:
        print(f"    - {os.path.relpath(p, source_directory)}")
else:
    print("  No folders will be skipped.")

while True:
    confirm = input(f"\n[{source_name}] files will be copied to [{folder_name}] at [{target_directory}]. Continue? (y/n): ").strip().lower()
    if confirm == 'y':
        break
    elif confirm == 'n':
        print("Cancelled.")
        exit()
    else:
        print("  Please enter 'y' or 'n'.")


# --- Copy ---
os.makedirs(target_directory, exist_ok=True)

for root, dirs, files in os.walk(source_directory):
    # Prune excluded dirs so os.walk won't descend into them
    dirs[:] = [d for d in dirs if os.path.join(root, d) not in excluded_paths]

    for name in files:
        if name == script_name:
            continue

        source_path = os.path.join(root, name)

        # Files in root keep their name; files in subfolders get a folder prefix
        if root == source_directory:
            new_name = name
        else:
            folder_prefix = os.path.basename(root)
            new_name = f"{folder_prefix}_{name}"

        destination_path = os.path.join(target_directory, new_name)

        # Handle filename collisions
        if os.path.exists(destination_path):
            base, ext = os.path.splitext(new_name)
            counter = 1
            while os.path.exists(destination_path):
                destination_path = os.path.join(target_directory, f"{base}_{counter}{ext}")
                counter += 1

        shutil.copy(source_path, destination_path)

print("\nFinished!")
