#!/usr/bin/env bash
# extract-changelog.sh - Extract release notes for a given version from CHANGELOG.md
#
# Usage: ./hack/extract-changelog.sh <version> [changelog-file]
#   version:        Semver tag (e.g., v1.5.2)
#   changelog-file: Path to CHANGELOG.md (default: CHANGELOG.md)
#
# Expects headings in Keep a Changelog format: ## [vX.Y.Z]
# Exits non-zero if no section is found for the given version.
set -euo pipefail

VERSION="${1:?Usage: extract-changelog.sh <version> [changelog-file]}"
CHANGELOG="${2:-CHANGELOG.md}"

if [ ! -f "${CHANGELOG}" ]; then
    echo "::error::Changelog file not found: ${CHANGELOG}" >&2
    exit 1
fi

# Escape regex metacharacters in version string for safe awk matching
ESCAPED_VERSION=$(printf '%s' "${VERSION}" | sed 's/[.[\(*^$+?{|\\]/\\&/g')

NOTES=$(awk "/^## \\[${ESCAPED_VERSION}\\]/{found=1; next} /^## \\[/{if(found) exit} found{print}" "${CHANGELOG}")

# Trim leading/trailing blank lines (portable across macOS and Linux)
NOTES=$(printf '%s' "${NOTES}" | awk 'NF{found=1} found' | awk '{lines[NR]=$0} END{if(NR>0){while(NR>0 && lines[NR]=="") NR--; for(i=1;i<=NR;i++) print lines[i]}}')

if [ -z "${NOTES}" ]; then
    echo "::error::No changelog entry found for ${VERSION} in ${CHANGELOG}" >&2
    echo "Expected a heading matching: ## [${VERSION}]" >&2
    exit 1
fi

echo "${NOTES}"
