#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Install bravesearch from source.

Usage:
  ./scripts/install.sh [--prefix DIR] [--bin-dir DIR] [--name NAME]

Options:
  --prefix DIR   Install prefix (default: ~/.local). Binary goes in DIR/bin.
  --bin-dir DIR  Install directly into DIR (overrides --prefix).
  --name NAME    Installed binary name (default: bravesearch).
  -h, --help     Show this help text.
EOF
}

PREFIX="${HOME}/.local"
BIN_DIR=""
BIN_NAME="bravesearch"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      [[ $# -ge 2 ]] || { echo "error: --prefix requires a value" >&2; exit 1; }
      PREFIX="$2"
      shift 2
      ;;
    --bin-dir)
      [[ $# -ge 2 ]] || { echo "error: --bin-dir requires a value" >&2; exit 1; }
      BIN_DIR="$2"
      shift 2
      ;;
    --name)
      [[ $# -ge 2 ]] || { echo "error: --name requires a value" >&2; exit 1; }
      BIN_NAME="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is not installed or not in PATH" >&2
  exit 1
fi

if [[ -z "${BIN_DIR}" ]]; then
  BIN_DIR="${PREFIX}/bin"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TARGET="${BIN_DIR}/${BIN_NAME}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

echo "Building bravesearch..."
(
  cd "${REPO_ROOT}"
  go build -o "${TMP_DIR}/bravesearch" ./cmd/bravesearch
)

mkdir -p "${BIN_DIR}"
cp "${TMP_DIR}/bravesearch" "${TARGET}"
chmod 0755 "${TARGET}"

echo "Installed: ${TARGET}"

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    echo
    echo "Note: ${BIN_DIR} is not currently in PATH."
    echo "Add it in your shell profile:"
    echo "  export PATH=\"${BIN_DIR}:\$PATH\""
    ;;
esac
