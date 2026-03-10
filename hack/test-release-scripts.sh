#!/usr/bin/env bash
# test-release-scripts.sh - Tests for extract-changelog.sh and validate-release.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TMPDIR=$(mktemp -d)
trap "rm -rf ${TMPDIR}" EXIT

PASSED=0
FAILED=0

pass() { PASSED=$((PASSED + 1)); echo "  ✅ $1"; }
fail() { FAILED=$((FAILED + 1)); echo "  ❌ $1"; }

# ---------------------------------------------------------------------------
# extract-changelog.sh tests
# ---------------------------------------------------------------------------
echo "Testing extract-changelog.sh"

# Create a test changelog
cat > "${TMPDIR}/CHANGELOG.md" <<'TESTEOF'
# Changelog

## [v1.6.0]

### What's Changed
- Added new feature X
- Fixed bug Y

## [v1.5.2]

### What's Changed
- Update base DCGM image to resolve CVEs

## [v1.5.1]

### What's Changed
- Initial release

[v1.6.0]: https://github.com/example/repo/compare/v1.5.2...v1.6.0
[v1.5.2]: https://github.com/example/repo/compare/v1.5.1...v1.5.2
TESTEOF

# Test 1: Extract existing version
OUTPUT=$("${SCRIPT_DIR}/extract-changelog.sh" "v1.5.2" "${TMPDIR}/CHANGELOG.md" 2>/dev/null)
if echo "${OUTPUT}" | grep -q "Update base DCGM image"; then
    pass "extracts notes for existing version"
else
    fail "extracts notes for existing version (got: ${OUTPUT})"
fi

# Test 2: Extract first version (no preceding section)
OUTPUT=$("${SCRIPT_DIR}/extract-changelog.sh" "v1.6.0" "${TMPDIR}/CHANGELOG.md" 2>/dev/null)
if echo "${OUTPUT}" | grep -q "Added new feature X"; then
    pass "extracts notes for first version in file"
else
    fail "extracts notes for first version in file (got: ${OUTPUT})"
fi

# Test 3: Fail on missing version
if "${SCRIPT_DIR}/extract-changelog.sh" "v9.9.9" "${TMPDIR}/CHANGELOG.md" > /dev/null 2>&1; then
    fail "should fail for missing version"
else
    pass "fails for missing version"
fi

# Test 4: Fail on missing changelog file
if "${SCRIPT_DIR}/extract-changelog.sh" "v1.5.2" "${TMPDIR}/nonexistent.md" > /dev/null 2>&1; then
    fail "should fail for missing file"
else
    pass "fails for missing changelog file"
fi

# Test 5: Fail when no version argument given
if "${SCRIPT_DIR}/extract-changelog.sh" 2>/dev/null; then
    fail "should fail with no arguments"
else
    pass "fails with no arguments"
fi

# Test 6: Version with dots is matched literally (not as regex wildcards)
cat > "${TMPDIR}/CHANGELOG-dots.md" <<'TESTEOF'
# Changelog

## [v1.5.2]

### What's Changed
- Real entry

## [v1X5Y2]

### What's Changed
- Fake entry from regex wildcard match
TESTEOF

OUTPUT=$("${SCRIPT_DIR}/extract-changelog.sh" "v1.5.2" "${TMPDIR}/CHANGELOG-dots.md" 2>/dev/null)
if echo "${OUTPUT}" | grep -q "Real entry" && ! echo "${OUTPUT}" | grep -q "Fake entry"; then
    pass "dots in version are matched literally"
else
    fail "dots in version are matched literally (got: ${OUTPUT})"
fi

# ---------------------------------------------------------------------------
# validate-release.sh tests
# ---------------------------------------------------------------------------
echo ""
echo "Testing validate-release.sh"

# Test 7: Pass with matching versions and changelog entry
cat > "${TMPDIR}/Chart.yaml" <<'TESTEOF'
apiVersion: v2
name: test-chart
version: 1.5.2
appVersion: 1.5.2
TESTEOF

if "${SCRIPT_DIR}/validate-release.sh" "${TMPDIR}/Chart.yaml" "${TMPDIR}/CHANGELOG.md" > /dev/null 2>&1; then
    pass "passes with matching versions and changelog"
else
    fail "passes with matching versions and changelog"
fi

# Test 8: Fail when version and appVersion mismatch
cat > "${TMPDIR}/Chart-mismatch.yaml" <<'TESTEOF'
apiVersion: v2
name: test-chart
version: 1.5.2
appVersion: 1.5.1
TESTEOF

if "${SCRIPT_DIR}/validate-release.sh" "${TMPDIR}/Chart-mismatch.yaml" "${TMPDIR}/CHANGELOG.md" > /dev/null 2>&1; then
    fail "should fail when version != appVersion"
else
    pass "fails when version != appVersion"
fi

# Test 9: Fail when changelog entry is missing for chart version
cat > "${TMPDIR}/Chart-new.yaml" <<'TESTEOF'
apiVersion: v2
name: test-chart
version: 9.9.9
appVersion: 9.9.9
TESTEOF

if "${SCRIPT_DIR}/validate-release.sh" "${TMPDIR}/Chart-new.yaml" "${TMPDIR}/CHANGELOG.md" > /dev/null 2>&1; then
    fail "should fail when changelog entry missing"
else
    pass "fails when changelog entry missing for chart version"
fi

# ---------------------------------------------------------------------------
# Validate against the real repo files
# ---------------------------------------------------------------------------
echo ""
echo "Testing against actual repo files"

REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
REAL_CHART="${REPO_ROOT}/charts/eks-node-monitoring-agent/Chart.yaml"
REAL_CHANGELOG="${REPO_ROOT}/CHANGELOG.md"

if [ -f "${REAL_CHART}" ] && [ -f "${REAL_CHANGELOG}" ]; then
    if "${SCRIPT_DIR}/validate-release.sh" "${REAL_CHART}" "${REAL_CHANGELOG}" > /dev/null 2>&1; then
        pass "real Chart.yaml and CHANGELOG.md are in sync"
    else
        fail "real Chart.yaml and CHANGELOG.md are NOT in sync"
    fi
else
    echo "  ⏩ Skipped (repo files not found)"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
TOTAL=$((PASSED + FAILED))
echo "Results: ${PASSED}/${TOTAL} passed, ${FAILED} failed"

if [ "${FAILED}" -gt 0 ]; then
    exit 1
fi
