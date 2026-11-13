#!/usr/bin/env bash

set -o nounset
set -o errexit
set -o pipefail

# update chart documentation using https://github.com/norwoodj/helm-docs
docker run --rm --volume "$(pwd):/helm-docs" \
    jnorwood/helm-docs:latest --ignore-non-descriptions

# shorten overly verbose default values.
# -i.bak + delete works on both GNU and BSD sed (BSD sed would treat `-i -e` as backup suffix)
find charts -type f -name README.md \
    -exec sed -i.bak -e 's#`.\{100,\}`#see [`values.yaml`](./values.yaml)#g' {} +
find charts -type f -name README.md.bak -delete
