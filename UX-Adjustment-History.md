# UX Adjustment History

## UX Improvements 1

- Should ask user if they are already in target directory or if they would like to select a different one. 
- If there are no subfolders detected, will tell user that they're already in a superdirectory. Will ask if they want to copy all files to a new directory anyway (y/n)
- Should be a confirmation after user input ([folder name] files will be copied to [target directory] at [path], continue? (y/n))
- The skip folders section should go one directory level at a time, not show all sub folders. So it will ask user what folders do you want to skip in [root]? Then will ask user what sub-folders they want to skip (excluding the previously skipped folders). Should also tell user to just press enter if they don't want to skip any subfolders. This continues until there are no more subfolders. 
- Add `finished!` notification once done. 
- Need an easier way to choose path where user wants files saved if possible. 
  - It should at least tell user it can't find that path if not found and ask them to check for typos and try again if not found. 

## UX Improvements 2

- How can we accomplish check boxes for folder selection?
- First asks if users they even want to exclude any subdirectories at all
- Way to go back during folder exclusion 
- Way to exclude all
- Loading indicator 

## UX Improvements 3

- Initial questions should use Questionary as well
- Clean up directory versus folder language (just use directory)
- Should be a way to exit the program at any time.
- Better way to choose target directory and save directory, ideally the user could hit tab to cycle through directories and/or autocomplete options for directory? Right now they need to copy paste to ensure correct path from scratch which isn't ideal. Open to other ideas. 
- Add white space or color coding between commands to avoid information overloads. Options between steps, such as skip all folder, select folder etc. should have bullets to be cleanly distinguished. 
- Folder selection should fill the bullet/bubble, text should change to red to indicate it will be skipped. non-skipped folders show as green. 
- Option to preview sub-directory folders (to know whether to skip or not). Once user is in preview, additional options to list files present in directory. They can choose to exclude or include right from preview or exit out of this preview without choosing. 
- Final confirmation should use Questionary, also if user selects "no" for final confirmation, asks if user wants to start over, go back to exclusion, or quit

## UX Improvement 4

- Should show user tip that they can use tab selection
  - Should also prompt user to start with "~" rather than the current folder since they already said it wasn't in the current folder
- Directories are showing with all this extra junk around it: `[^[[36m*****^[[0m]:`, please fix that
  - It's showing in other places too such as: `^[[2m(space = toggle · checked/red = skipped · enter = confirm)^[[0m`
- During exclusion don't highlight red, turn the text red. 
- The preview menu is good, on a "Select directories to SKIP" step there should be an option for p = preview as well with same functionality 
  - Previews currently highlighted directory
- These instructions should be combined, they're separate right now: `^[[2m(space = toggle · checked/red = skipped · enter = confirm)^[[0m (Use arrow keys to move, <space> to select, <a> to toggle, <i> to invert)` 
- Change Go back one level and start over icons to emojis (right now they don't match the skip/preview options)
  - also change "Skip remaining exclusion checks" to fast-foward to end emoji
- The following three options should have icons instead of bubbles (on this step: "Subdirectories in [directory]"):
  - "Select directories to skip" - finger point up emoji 
  - "Skip ALL directories here" - fast forward emoji
  - "Keep ALL directories (continue)" - white check mark on green background emoji
- Edit text for final confirmation: (currently: "Excluding 30 director(ies):")
  - If user is only excluding one directory: "Excluding 1 directory:"
  - If user is excluding multiple directories: Excluding [number] directories:

## UX Improvement 5

- Need to make it more obvious when tab complete is selected, perhaps show it inline?
- When user types ~ during path selection, if they confirm the autocomplete to their homedirectory, should automatically put the forward slash after the ~ 
- For this step:  `Would you like to exclude any subdirectories? (Y/n)` please make the word "exclude" text red
- For this text: `(↑↓ navigate · space toggle · enter confirm · Esc back to menu)` should have indicator to show button = command. Such as `space = toggle`
- We're missing the `p` to preview command I previously requested within the `Select directories to SKIP  (red = skipped) (↑↓ navigate · space toggle · enter confirm · Esc back to menu)` step
  - Original request: the preview menu is good, on a "Select directories to SKIP" step there should be an option for `p = preview` as well with same functionality 
  - `⏭️   Skip remaining exclusion checks` too much spacing between emoji and text
- The Esc back to menu doesn't appear to be doing anything. It should essentially act as a back button.
  - For example, I'm on a Select directories to SKIP step (which gives Esc as an option in hint) but when I hit it nothing happens. It should go back to the What would you like to do with [directory] step. 
 - In the final confirmation, the last directory in the path should be in red font. 
  - For example: Some_Docs/Another_Folder/Yet_Another_Folder/Last_Should_Be_Red
- In the final confirmation, user should have another option to instead carry on with the copying process. (In case they accidentally selected No)
  - Also, if they select to `go back to exclusion settings` they should be prompted if they want to keep previous exclusions or remove them. 

## UX Improvement 6

- For directory selection tip: Add `enter` confirms auto-complete. Edit so it says Tab+arrow keys to complete paths
- Garbling of text in some areas such as: Would you like to ^[[31mexclude^[[0m any subdirectories?
- For sub-directory exclusion, maintain subdirectory in options
  - Select "subdirectories" to skip
  - Skip all "subdirectories"
  - and so on 
- If user selects no at confirmation, add an option in the "What would you like to do" menu to copy to a different directory 
- Above the Ctrl-C exit at any time dialouge add ASCII WELCOME to SUPERDIRECTORY art
- Make the [subdirectory] currently up for or in exclusion in orange font