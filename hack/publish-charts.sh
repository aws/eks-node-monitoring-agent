#!/usr/bin/env bash
set -euo pipefail

GIT_REPO_ROOT=$(git rev-parse --show-toplevel)
VERSION=$(git describe --tags --always)

STAGING_DIR=${1:-${GIT_REPO_ROOT}}
CHART_URL=${2:-https://aws.github.io/eks-node-monitoring-agent}

helm package ${GIT_REPO_ROOT}/charts/* --destination ${STAGING_DIR} --dependency-update
git checkout gh-pages
mv ${STAGING_DIR}/*.tgz .
helm repo index . --url ${CHART_URL}
git add index.yaml *.tgz
git commit -m "Publish charts ${VERSION}"
git push origin gh-pages
echo "✅ Published charts"
