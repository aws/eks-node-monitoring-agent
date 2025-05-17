#!/usr/bin/env bash

set -o nounset
set -o errexit
set -o pipefail

# update chart documentation using https://github.com/norwoodj/helm-docs
docker run --rm --volume "$(pwd):/helm-docs" \
    jnorwood/helm-docs:latest --ignore-non-descriptions

# shorten overly verbose default values.
find charts -type f -name README.md \
    -exec sed -i -e 's#`.\{100,\}`#see [`values.yaml`](./values.yaml)#g' {} +\
