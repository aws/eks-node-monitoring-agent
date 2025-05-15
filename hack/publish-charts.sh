#!/usr/bin/env bash
set -euo pipefail

GIT_REPO_ROOT=$(git rev-parse --show-toplevel)
VERSION=$(git describe --tags --always)

STAGING_DIR=${1:-${GIT_REPO_ROOT}}
CHART_URL=${2:-https://aws.github.io/eks-node-monitoring-agent}

git fetch --all
git config user.email eks-bot@users.noreply.github.com
git config user.name eks-bot

helm package ${GIT_REPO_ROOT}/charts/* --destination ${STAGING_DIR} --dependency-update

for bundle in ${STAGING_DIR}/*.tgz; do
    if git cat-file -e origin/gh-pages:${bundle}; then
        echo "⏩ Release already exists for ${bundle}"
        rm -f ${bundle}
    fi
done

if ! ls ${STAGING_DIR}/*.tgz; then
    echo "⏩ No changes to be staged"
    exit 0
fi

git checkout gh-pages
mv ${STAGING_DIR}/*.tgz .
helm repo index . --url ${CHART_URL}
git add index.yaml *.tgz
git commit -m "Publish charts ${VERSION}"
git push origin gh-pages
echo "✅ Published charts"
