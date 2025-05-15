#!/usr/bin/env bash

make generate
if ! git diff --exit-code .; then
    echo >&2 "❌ generated code is out of date. Please run 'make generate' and commit the changes."
    exit 1
fi

echo "✅ generated code is up to date."
