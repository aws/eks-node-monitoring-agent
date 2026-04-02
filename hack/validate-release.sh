#!/usr/bin/env bash
# validate-release.sh - Validate that Chart.yaml version and appVersion match,
# and that CHANGELOG.md has an entry for the current chart version.
#
# Usage: ./hack/validate-release.sh [chart-yaml] [changelog-file]
#
# This script is run in CI (pr-check) to catch issues before a release tag is pushed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

CHART_YAML="${1:-${REPO_ROOT}/charts/eks-node-monitoring-agent/Chart.yaml}"
CHANGELOG="${2:-${REPO_ROOT}/CHANGELOG.md}"

ERRORS=0

# Extract versions from Chart.yaml
CHART_VERSION=$(grep '^version:' "${CHART_YAML}" | awk '{print $2}')
APP_VERSION=$(grep '^appVersion:' "${CHART_YAML}" | awk '{print $2}')

if [ -z "${CHART_VERSION}" ]; then
    echo "::error::Could not read version from ${CHART_YAML}"
    ERRORS=$((ERRORS + 1))
fi

if [ -z "${APP_VERSION}" ]; then
    echo "::error::Could not read appVersion from ${CHART_YAML}"
    ERRORS=$((ERRORS + 1))
fi

# Validate version and appVersion match
if [ "${CHART_VERSION}" != "${APP_VERSION}" ]; then
    echo "::error::Chart.yaml version (${CHART_VERSION}) does not match appVersion (${APP_VERSION})"
    ERRORS=$((ERRORS + 1))
else
    echo "✅ Chart.yaml version and appVersion match: ${CHART_VERSION}"
fi

# Validate CHANGELOG.md has an entry for this version
VERSION="v${CHART_VERSION}"
if "${SCRIPT_DIR}/extract-changelog.sh" "${VERSION}" "${CHANGELOG}" > /dev/null 2>&1; then
    echo "✅ CHANGELOG.md has entry for ${VERSION}"
else
    echo "::error::CHANGELOG.md is missing entry for ${VERSION}"
    echo "   Expected a heading matching: ## [${VERSION}]"
    ERRORS=$((ERRORS + 1))
fi

if [ "${ERRORS}" -gt 0 ]; then
    echo ""
    echo "❌ Release validation failed with ${ERRORS} error(s)"
    exit 1
fi

echo ""
echo "✅ Release validation passed"
