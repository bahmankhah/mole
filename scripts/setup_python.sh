#!/usr/bin/env bash
# Setup script for Mole crawler Python dependencies.
# Creates a virtual environment under scripts/.venv and installs all deps.
# The Go server auto-detects the venv — no need to "activate" it manually.
#
# Usage:
#   chmod +x scripts/setup_python.sh
#   ./scripts/setup_python.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VENV_DIR="$SCRIPT_DIR/.venv"
REQ_FILE="$SCRIPT_DIR/requirements.txt"

echo "==> Mole Python dependency installer"
echo ""

# ---------- locate Python 3 ----------
PYTHON=""
for candidate in python3 python; do
    if command -v "$candidate" &>/dev/null; then
        version=$("$candidate" --version 2>&1 | grep -oP '\d+\.\d+')
        major=$(echo "$version" | cut -d. -f1)
        if [ "$major" -ge 3 ]; then
            PYTHON="$candidate"
            break
        fi
    fi
done

if [ -z "$PYTHON" ]; then
    echo "ERROR: Python 3 is required but not found."
    echo "Install it with:  sudo apt install python3 python3-venv   (Debian/Ubuntu)"
    echo "                  sudo dnf install python3                (Fedora)"
    echo "                  brew install python                     (macOS)"
    exit 1
fi

echo "Using Python: $PYTHON ($($PYTHON --version))"

# ---------- ensure python3-venv is available ----------
if ! $PYTHON -m venv --help &>/dev/null; then
    echo ""
    echo "python3-venv module not found — installing it ..."
    if command -v apt-get &>/dev/null; then
        sudo apt-get update -qq && sudo apt-get install -y python3-venv
    elif command -v dnf &>/dev/null; then
        sudo dnf install -y python3-libs  # venv included on Fedora
    else
        echo "ERROR: Cannot install python3-venv automatically. Please install it manually."
        exit 1
    fi
fi

# ---------- create venv ----------
echo ""
if [ -d "$VENV_DIR" ]; then
    echo "==> Virtual environment already exists at $VENV_DIR"
else
    echo "==> Creating virtual environment at $VENV_DIR ..."
    $PYTHON -m venv "$VENV_DIR"
fi

# Activate venv for the rest of this script
source "$VENV_DIR/bin/activate"
PIP="$VENV_DIR/bin/pip"
VPYTHON="$VENV_DIR/bin/python3"

echo "   venv python: $VPYTHON"

# ---------- pip install ----------
echo ""
echo "==> Upgrading pip ..."
"$PIP" install --upgrade pip

echo ""
echo "==> Installing Python packages from $REQ_FILE ..."
"$PIP" install -r "$REQ_FILE"

# ---------- Playwright browsers ----------
echo ""
echo "==> Installing Playwright Chromium browser ..."
# Try with system deps first; fall back to browser-only if apt has conflicts
"$VPYTHON" -m playwright install --with-deps chromium 2>/dev/null \
    || {
        echo "   --with-deps failed (apt conflict?), installing browser only ..."
        "$VPYTHON" -m playwright install chromium
        echo "   NOTE: If headless fetching fails later, install system deps manually:"
        echo "         sudo npx playwright install-deps chromium"
        echo "     or: sudo apt install libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxrandr2 libgbm1 libpango-1.0-0 libcairo2 libasound2t64"
    }

# ---------- Pre-download sentence-transformers model ----------
echo ""
echo "==> Pre-downloading sentence-transformer model (paraphrase-multilingual-MiniLM-L12-v2) ..."
"$VPYTHON" -c "
from sentence_transformers import SentenceTransformer
model = SentenceTransformer('paraphrase-multilingual-MiniLM-L12-v2')
print(f'Model loaded — dimension: {model.get_sentence_embedding_dimension()}')
"

# ---------- Verify ----------
echo ""
echo "==> Verifying installations ..."
"$VPYTHON" -c "from importlib.metadata import version; print(f'  playwright           {version(\"playwright\")}')"
"$VPYTHON" -c "import sentence_transformers; print(f'  sentence-transformers {sentence_transformers.__version__}')"
"$VPYTHON" -c "import faiss; print(f'  faiss-cpu            {faiss.__version__}')" 2>/dev/null \
    || "$VPYTHON" -c "import faiss; print('  faiss-cpu            OK')"
"$VPYTHON" -c "import numpy; print(f'  numpy                {numpy.__version__}')"

echo ""
echo "==> All done! Python venv is ready at: $VENV_DIR"
echo "    The Go server will auto-detect it — no need to activate manually."
