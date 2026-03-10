# Releasing

This document describes how to create a new release of the EKS Node Monitoring Agent.

## Prerequisites

- Push access to the `aws/eks-node-monitoring-agent` repository
- Permissions to create tags and GitHub releases

## Release process

### 1. Update version and changelog

In a PR against `main`, update the following files:

- **`charts/eks-node-monitoring-agent/Chart.yaml`**: Bump both `version` and `appVersion` to the new version (they must match).
- **`CHANGELOG.md`**: Add a new section for the release using [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format:
  ```markdown
  ## [v1.6.0]

  ### What's Changed
  - Description of change ([commit](link))

  [v1.6.0]: https://github.com/aws/eks-node-monitoring-agent/compare/v1.5.2...v1.6.0
  ```

The `validate-release` CI check on every PR verifies that:
- `version` and `appVersion` in `Chart.yaml` match
- `CHANGELOG.md` has an entry for the current chart version

### 2. Merge the PR

Once the version bump PR is reviewed and merged to `main`, the `Publish Charts` workflow automatically packages and publishes the Helm chart to the `gh-pages` branch.

### 3. Create and push the tag

After the PR is merged, create an annotated tag on `main` and push it:

```bash
git checkout main
git pull origin main
git tag -a v1.6.0 -m "Release v1.6.0"
git push origin v1.6.0
```

### 4. Automatic release creation

Pushing the tag triggers the `[Release] Create GitHub Release` workflow, which:

1. Validates the tag version matches `Chart.yaml` `version` and `appVersion`
2. Extracts release notes from the `CHANGELOG.md` section for that version
3. Creates a GitHub release with those notes

The release will appear at `https://github.com/aws/eks-node-monitoring-agent/releases`.

## Validation scripts

The release tooling lives in `hack/` and can be run locally:

```bash
# Validate Chart.yaml versions match and CHANGELOG.md has the right entry
hack/validate-release.sh

# Extract changelog notes for a specific version
hack/extract-changelog.sh v1.5.2

# Run all release script tests
hack/test-release-scripts.sh
```

## Troubleshooting

- **Release workflow fails with "Tag version does not match Chart.yaml version"**: The tag you pushed doesn't match the version in `Chart.yaml`. Ensure the version bump PR was merged before tagging.
- **Release workflow fails with "No changelog entry found"**: Add a `## [vX.Y.Z]` section to `CHANGELOG.md` and merge it before tagging.
- **`validate-release` CI check fails on a PR**: Either `version` and `appVersion` in `Chart.yaml` don't match, or `CHANGELOG.md` is missing an entry for the current chart version. Fix both before merging.
