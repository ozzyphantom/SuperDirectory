import os
import shutil

script_name = os.path.basename(__file__)

# Ask where to save the new folder
base_path = input("Where do you want your SuperDirectory? (e.g. ~/Desktop/Homework):")
# Expand ~ to the full path
base_path = os.path.expanduser(base_path)


# Name the folder
folder_name = input("What're you naming your SuperDirectory?: (e.g. totally legally aquired PDFs)")

# Combine the paths
target_directory = os.path.join(base_path, folder_name)

print(f"Files will be copied to: {target_directory}")

# create empty list to store subfolder names
all_subfolders = []

# walk the current folder
for root, dirs, files in os.walk('.'):
        for name in dirs:
                # Create a clean path from the current location to the subfolder
                folder_path = os.path.join(root, name)
                all_subfolders.append(folder_path)

# Display them to the user
print("\nSubfolders found:")
for i, path in enumerate(all_subfolders):
        print(f"{i}: {path}")

# User selects which files to skip
while True:
    choices = input("What folders would you like to skip? Enter number of folder followed by a comma (e.g. 1, 2, 5, 6):")
    if not choices.strip():
            excluded_paths = []
            break
    try:
        indices = [int(i.strip()) for i in choices.split(",")]

        if all(0 <= i < len(all_subfolders) for i in indices):
               excluded_paths = [all_subfolders[i] for i in indices]
               break
        else:
               print(f"Please use numbers between 0 and {len(all_subfolders) - 1}.")
    except ValueError:
           print("Invalid input. Please enter numbers separated by commas.")

# ensures no overwritten files
folder_name = os.path.basename(root)
new_filename = f"{folder_name}_{name}"

# Create target directory if it doesn't exist
os.makedirs(target_directory, exist_ok=True)

for root, dirs, files in os.walk('.'):
        # check if 'root' is is inside any excluded paths
        if any(root.startswith(ex) for ex in excluded_paths):
                continue # Skip the folder

        for name in files:
                # Make sure script doesn't copy itself over
                if name == script_name:
                        continue
                source_path = os.path.join(root, name)

                # Create the new name using the parent folder
                folder_prefix = os.path.basename(root)
                new_name = f"{folder_prefix}_{name}"
                destination_path = os.path.join(target_directory, new_name)

                # Copy the file!
                shutil.copy(source_path, destination_path)
