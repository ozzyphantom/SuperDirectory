#!/usr/bin/env bash
set -e

cd "$(dirname "$0")"

if [ ! -d "venv" ]; then
    echo "Creating virtual environment..."
    python3 -m venv venv
fi

source venv/bin/activate

if ! python -c "import questionary, prompt_toolkit" 2>/dev/null; then
    echo "Installing dependencies..."
    pip install -r requirements.txt
fi

python SuperDirectory.py "$@"
