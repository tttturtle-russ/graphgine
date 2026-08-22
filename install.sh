#!/usr/bin/env bash
#
# install.sh — build & install graphgine + its Claude Code skill.
#
# Installs:
#   1. the `graphgine` binary            -> $BIN_DIR            (default ~/.local/bin)
#   2. the `graphgine` driver skill      -> $CLAUDE_DIR/skills  (default ~/.claude/skills)
#   3. the `sec-graph` node-worker agent -> $CLAUDE_DIR/agents  (default ~/.claude/agents)
#   4. the bundled node skills           -> $SKILLS_DIR         (default ~/.claude/skills)
#      They go in the host's own skills dir so nodes can *bind* them by name
#      (Skill tool / agent `skills:` frontmatter) instead of pasting their text.
#
# Usage:
#   ./install.sh [--bin-dir DIR] [--claude-dir DIR] [--skills-dir DIR] [--uninstall]
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

BIN_DIR="${GRAPHGINE_BIN_DIR:-$HOME/.local/bin}"
CLAUDE_DIR="${CLAUDE_HOME:-$HOME/.claude}"
SKILLS_DIR="${GRAPHGINE_SKILLS_DIR:-}"   # defaults to $CLAUDE_DIR/skills once flags are parsed
DO_UNINSTALL=0

BOLD=$'\033[1m'; DIM=$'\033[2m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; RST=$'\033[0m'
info()  { printf '%s==>%s %s\n' "$GREEN" "$RST" "$*"; }
warn()  { printf '%s warn:%s %s\n' "$YELLOW" "$RST" "$*"; }
die()   { printf '%serror:%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

usage() {
  sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir)    BIN_DIR="${2:?}"; shift 2 ;;
    --claude-dir) CLAUDE_DIR="${2:?}"; shift 2 ;;
    --skills-dir) SKILLS_DIR="${2:?}"; shift 2 ;;
    --uninstall)  DO_UNINSTALL=1; shift ;;
    -h|--help)    usage ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

# bundled node skills, by folder name — installed into and removed from $SKILLS_DIR
BUNDLED_SKILLS=()
if [ -d "$REPO_DIR/skills" ]; then
  for d in "$REPO_DIR"/skills/*/; do
    [ -f "$d/SKILL.md" ] && BUNDLED_SKILLS+=("$(basename "$d")")
  done
fi

SKILLS_DIR="${SKILLS_DIR:-$CLAUDE_DIR/skills}"

SKILL_DEST="$CLAUDE_DIR/skills/graphgine"
AGENT_DEST="$CLAUDE_DIR/agents/sec-graph.md"
BIN_DEST="$BIN_DIR/graphgine"

if [ "$DO_UNINSTALL" = 1 ]; then
  info "Uninstalling graphgine"
  rm -f  "$BIN_DEST"                 && echo "  removed $BIN_DEST"
  rm -rf "$SKILL_DEST"               && echo "  removed $SKILL_DEST"
  rm -f  "$AGENT_DEST"               && echo "  removed $AGENT_DEST"
  # only the skills we shipped — $SKILLS_DIR is the user's own skills dir by default
  for name in ${BUNDLED_SKILLS+"${BUNDLED_SKILLS[@]}"}; do
    rm -rf "$SKILLS_DIR/$name"       && echo "  removed $SKILLS_DIR/$name"
  done
  info "Done."
  exit 0
fi

command -v go >/dev/null 2>&1 || die "Go toolchain not found. Install Go, then re-run."

# 1. build + install the binary
info "Building graphgine binary"
mkdir -p "$BIN_DIR"
( cd "$REPO_DIR" && go build -o "$BIN_DEST" ./cmd/graphgine )
echo "  installed ${BOLD}$BIN_DEST${RST}"

# 2. install the Claude Code driver skill
info "Installing driver skill for Claude Code"
mkdir -p "$SKILL_DEST"
cp "$REPO_DIR/.claude/skills/graphgine/SKILL.md" "$SKILL_DEST/SKILL.md"
echo "  installed ${BOLD}$SKILL_DEST/SKILL.md${RST}"

# 3. install the sec-graph node-worker agent (so node sub-agents show as "sec-graph")
info "Installing sec-graph node-worker agent for Claude Code"
mkdir -p "$CLAUDE_DIR/agents"
cp "$REPO_DIR/.claude/agents/sec-graph.md" "$AGENT_DEST"
echo "  installed ${BOLD}$AGENT_DEST${RST}"

# 4. install the bundled node skills into the host's skills dir, so a node can
#    bind them by name instead of the driver pasting their text into a prompt
if [ ${#BUNDLED_SKILLS[@]} -gt 0 ]; then
  info "Installing bundled node skills"
  mkdir -p "$SKILLS_DIR"
  for name in "${BUNDLED_SKILLS[@]}"; do
    rm -rf "$SKILLS_DIR/$name"
    cp -R "$REPO_DIR/skills/$name" "$SKILLS_DIR/$name"
    echo "  installed ${BOLD}$SKILLS_DIR/$name${RST}"
  done
fi

# post-install guidance
echo
info "Installed. Next steps:"
case ":$PATH:" in
  *":$BIN_DIR:"*) : ;;
  *) warn "$BIN_DIR is not on your PATH. Add it:"
     echo "     ${DIM}export PATH=\"$BIN_DIR:\$PATH\"${RST}" ;;
esac
if [ "$SKILLS_DIR" != "$CLAUDE_DIR/skills" ]; then
  echo "  • Bundled skills live outside the host's skills dir, so they cannot be"
  echo "    bound by name. Point the driver at them to resolve them by path:"
  echo "     ${DIM}export GRAPHGINE_SKILLS_DIR=\"$SKILLS_DIR\"${RST}"
fi
echo "  • In Claude Code, ask it to \"run a graphgine graph\" — the ${BOLD}graphgine${RST} skill drives it."
echo "  • Verify: ${DIM}graphgine --help${RST}"
