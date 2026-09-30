A set of cli scripts to help organize the photos from google takeout mass downloads

All scripts can be run with `uv run <script_name>`

1. `organize-takeout.py`: This script will organize the photos from a Google Takeout download into folders based on the date the photo was taken. It will create a folder for each year and month, and move the photos into the appropriate folder. The key feature of this script is it unzips the downloaded takeout files and organizes the photos automatically. ex. `uv run organize-takeout.py /path/to/takeout /path/to/output/folder`

2. `organize-existing.py`: This script will organize photos that are already in a folder structure. It will move the photos into folders based on the date the photo was taken, creating a folder for each year and month. ex. `uv run organize-existing.py /path/to/existing/photos /path/to/output/folder`