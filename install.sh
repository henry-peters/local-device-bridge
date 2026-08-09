#!/usr/bin/env bash
# Install local-device-bridge on macOS or Linux.
#
# The script deliberately uses only tools normally present on those systems:
# bash, curl, tar, and (when building from source) Go. The CLI visual layer is
# compiled into the binary; there is no Node, Python, npm, or runtime package
# installation.
set -euo pipefail

REPOSITORY="${LDB_REPOSITORY:-local-device-bridge/local-device-bridge}"
INSTALL_DIR="${LDB_INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${LDB_VERSION:-latest}"
FROM_SOURCE="${LDB_FROM_SOURCE:-0}"
NO_SETUP="${LDB_NO_SETUP:-0}"

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'HELP'
local-device-bridge installer

Usage:
  ./install.sh                 install the latest release, then open setup
  ./install.sh --from-source   build the checked-out repository with Go
  ./install.sh --no-setup      install without starting the setup wizard

Environment overrides:
  LDB_REPOSITORY=owner/repo    GitHub repository (default: local-device-bridge/local-device-bridge)
  LDB_INSTALL_DIR=/path        binary destination (default: ~/.local/bin)
  LDB_VERSION=v0.1.3           release tag; latest is used by default
  LDB_FROM_SOURCE=1             build from the current checkout
  LDB_NO_SETUP=1                skip the interactive setup wizard
HELP
  exit 0
fi

for argument in "$@"; do
  case "$argument" in
    --from-source) FROM_SOURCE=1 ;;
    --no-setup) NO_SETUP=1 ;;
    --help|-h) exec "$0" --help ;;
    *) printf 'Unknown option: %s (run ./install.sh --help)\n' "$argument" >&2; exit 1 ;;
  esac
done

if [[ -t 1 && "${NO_COLOR:-}" == "" && "${TERM:-}" != "dumb" ]]; then
  CYAN=$'\033[36m'; BLUE=$'\033[34m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  CYAN=''; BLUE=''; GREEN=''; YELLOW=''; RED=''; BOLD=''; RESET=''
fi

say() { printf '%b\n' "$*"; }
step() { say "${CYAN}→${RESET} ${BOLD}$1${RESET}"; }
ok() { say "${GREEN}✓${RESET} $1"; }
warn() { say "${YELLOW}⚠${RESET} $1"; }
fail() { say "${RED}✗${RESET} $1" >&2; exit 1; }

say ""
say "${CYAN}────────────────────────────────────────────────────────────────────────${RESET}"
say "  ${BOLD}INSTALLER${RESET}  ${BLUE}//  FIRST RUN SETUP${RESET}"
say "  local-device-bridge  ·  ${YELLOW}Discover  •  Pair  •  Control  •  Audit${RESET}"
say "${CYAN}────────────────────────────────────────────────────────────────────────${RESET}"
say ""

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$OS" in
  darwin) OS="darwin" ;;
  linux) OS="linux" ;;
  *) fail "This installer supports macOS and Linux. Windows users: run install.ps1 in PowerShell." ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) fail "Unsupported CPU architecture: $ARCH" ;;
esac
ok "Platform detected: ${OS}/${ARCH}"

command -v mkdir >/dev/null || fail "mkdir is required"
command -v chmod >/dev/null || fail "chmod is required"
command -v mktemp >/dev/null || fail "mktemp is required"
command -v install >/dev/null || fail "install is required"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/local-device-bridge.XXXXXX")"
cleanup() { rm -rf "$TEMP_DIR"; }
trap cleanup EXIT

SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${INSTALL_DIR}/local-device-bridge"

build_source() {
  command -v go >/dev/null || fail "Go is required for a source install. Install Go 1.26+ or use a published release artifact."
  [[ -f "${SOURCE_DIR}/go.mod" ]] || fail "--from-source must be run from the repository root"
  step "Building the self-contained CLI"
  (cd "$SOURCE_DIR" && go build -trimpath -ldflags='-s -w' -o "${TEMP_DIR}/local-device-bridge" ./cmd/local-device-bridge)
  ok "Binary built without external UI packages"
}

download_release() {
  command -v curl >/dev/null || fail "curl is required to download a release"
  command -v tar >/dev/null || fail "tar is required to unpack a release"
  local tag="$VERSION"
  if [[ "$tag" == "latest" ]]; then
    step "Finding the latest published release"
    tag="$(curl --fail --silent --show-error --location --retry 2 "https://api.github.com/repos/${REPOSITORY}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    [[ -n "$tag" ]] || fail "No published release was found for ${REPOSITORY}. Clone the repository and run ./install.sh --from-source."
  fi
  local archive="local-device-bridge_${OS}_${ARCH}.tar.gz"
  step "Downloading ${REPOSITORY} ${tag}"
  curl --fail --silent --show-error --location --retry 2 --proto '=https' --tlsv1.2 \
    -o "${TEMP_DIR}/${archive}" \
    "https://github.com/${REPOSITORY}/releases/download/${tag}/${archive}"
  curl --fail --silent --show-error --location --retry 2 --proto '=https' --tlsv1.2 \
    -o "${TEMP_DIR}/checksums.txt" \
    "https://github.com/${REPOSITORY}/releases/download/${tag}/checksums.txt"
  local expected actual
  expected="$(awk -v file="$archive" '$2 == file { print $1; exit }' "${TEMP_DIR}/checksums.txt")"
  [[ "$expected" =~ ^[[:xdigit:]]{64}$ ]] || fail "The release checksum did not list ${archive}"
  if command -v sha256sum >/dev/null; then
    actual="$(sha256sum "${TEMP_DIR}/${archive}" | awk '{print $1}')"
  elif command -v shasum >/dev/null; then
    actual="$(shasum -a 256 "${TEMP_DIR}/${archive}" | awk '{print $1}')"
  else
    fail "sha256sum or shasum is required to verify the release"
  fi
  [[ "$actual" == "$expected" ]] || fail "The downloaded release checksum did not match"
  ok "Release checksum verified"
  tar -xzf "${TEMP_DIR}/${archive}" -C "$TEMP_DIR"
  [[ -f "${TEMP_DIR}/local-device-bridge" ]] || fail "The release did not contain the expected local-device-bridge binary"
  ok "Release downloaded"
}

if [[ "$FROM_SOURCE" == "1" ]]; then
  build_source
else
  download_release
fi

step "Installing to ${INSTALL_DIR}"
mkdir -p "$INSTALL_DIR"
install -m 0755 "${TEMP_DIR}/local-device-bridge" "$TARGET"
ok "Installed ${TARGET}"

if [[ ":${PATH}:" != *":${INSTALL_DIR}:"* ]]; then
  warn "${INSTALL_DIR} is not on PATH for this shell"
  say "  Add it with: export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

SETUP_RAN=0
if [[ "$NO_SETUP" == "1" || ! -t 1 || ( ! -t 0 && ! -r /dev/tty ) ]]; then
  say ""
  say "Installation is complete. Setup was skipped for this non-interactive install."
  say "Run ${TARGET} setup later if you want to configure the bridge interactively."
else
  say ""
  step "Opening the first-run setup wizard"
  say "Use ↑/↓ and Enter to choose each option."
  SETUP_RAN=1
  # `curl ... | bash` has a pipe on stdin even though the user is sitting at
  # a terminal. Reattach the wizard to /dev/tty so the published installer
  # remains interactive and can show its phone QR code.
  if [[ -t 0 ]]; then
    "$TARGET" setup
  else
    "$TARGET" setup < /dev/tty
  fi
fi

say ""
say "${GREEN}${BOLD}Installation complete.${RESET}"
if [[ "$SETUP_RAN" != "1" ]]; then
  say "No daemon or browser was started because setup was skipped."
else
  say "If setup selected automatic dashboard launch, the daemon and browser were started."
  say "The setup wizard already handled the selected startup mode."
fi
say "Run ${TARGET} with no command for the interactive CLI home."
