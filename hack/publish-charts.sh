#!/usr/bin/env bash
set -euo pipefail

GIT_REPO_ROOT=$(git rev-parse --show-toplevel)
VERSION=$(git describe --tags --always)

STAGING_DIR=${1:-${GIT_REPO_ROOT}}
CHART_URL=${2:-https://aws.github.io/eks-node-monitoring-agent}

# If CHART_COMMIT is set, extract charts from that commit instead of current HEAD
CHART_COMMIT=${CHART_COMMIT:-}

git fetch --all
git config user.email eks-bot@users.noreply.github.com
git config user.name eks-bot

if [ -n "${CHART_COMMIT}" ]; then
    echo "Packaging charts from commit ${CHART_COMMIT}"
    TEMP_CHART_DIR=$(mktemp -d)
    trap "rm -rf ${TEMP_CHART_DIR}" EXIT
    
    # Extract each chart directory from the specified commit
    for chart in $(git ls-tree --name-only ${CHART_COMMIT} charts/); do
        chart_name=$(basename ${chart})
        mkdir -p "${TEMP_CHART_DIR}/${chart_name}"
        git archive ${CHART_COMMIT}:${chart} | tar -x -C "${TEMP_CHART_DIR}/${chart_name}"
    done
    
    helm package ${TEMP_CHART_DIR}/* --destination ${STAGING_DIR} --dependency-update
else
    helm package ${GIT_REPO_ROOT}/charts/* --destination ${STAGING_DIR} --dependency-update
fi

for bundle in ${STAGING_DIR}/*.tgz; do
    bundle_name=$(basename ${bundle})
    if git cat-file -e origin/gh-pages:${bundle_name} 2>/dev/null; then
        echo "⏩ Release already exists for ${bundle_name}"
        rm -f ${bundle}
    fi
done

if ! ls ${STAGING_DIR}/*.tgz; then
    echo "⏩ No changes to be staged"
    exit 0
fi

git checkout gh-pages

if [ "$(realpath ${STAGING_DIR})" != "$(realpath .)" ]; then
    mv ${STAGING_DIR}/*.tgz .
fi

helm repo index . --url ${CHART_URL}
git add index.yaml *.tgz
git commit -m "Publish charts ${VERSION}"
git push origin gh-pages
echo "✅ Published charts"
