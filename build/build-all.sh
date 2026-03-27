#!/bin/bash

export TZ="Australia/Queensland"
export GOTOOLCHAIN="auto"

BUILD_DATE="$(date -u +"%Y-%m-%dT%H:%M:%S%z")"
BUILD_GO_VERSION="$(go version | sed -e 's/.* go/go/;s/ .*//')"
BUILD_GIT_COMMIT="$(git rev-parse --short HEAD)"
BUILD_GIT_REPO="$(git config --get remote.origin.url)"
BUILD_GIT_TAG="$(git describe --tags --abrev=0 2>/dev/null || true)"
BUILD_GIT_EXACT_TAG="$(git describe --tags --exact-match 2>/dev/null || true)"
BUILD_GIT_SLUG="na4ma4/hostcheck"
BUILD_VERSION="$(git describe --tags HEAD)"

BUILD_ARGS=(
    -tags=release
    -trimpath
    -v
    -ldflags
    "-X github.com/dosquad/go-cliversion.BuildDate=${BUILD_DATE} -X github.com/dosquad/go-cliversion.BuildDebug=false -X github.com/dosquad/go-cliversion.BuildMethod=github-actions -X github.com/dosquad/go-cliversion.BuildGoVersion=${BUILD_GO_VERSION} -X github.com/dosquad/go-cliversion.GitCommit=${BUILD_GIT_COMMIT} -X github.com/dosquad/go-cliversion.GitRepo=${BUILD_GIT_REPO} -X github.com/dosquad/go-cliversion.GitSlug=${BUILD_GIT_SLUG} -X github.com/dosquad/go-cliversion.GitTag=${BUILD_GIT_TAG} -X github.com/dosquad/go-cliversion.GitExactTag=${BUILD_GIT_EXACT_TAG} -X main.commit=${BUILD_GIT_COMMIT} -X main.date=${BUILD_DATE} -X main.builtBy=magefiles -X main.repo=${BUILD_GIT_REPO} -X main.goVersion=${BUILD_GO_VERSION} -X main.version=${BUILD_VERSION} -X github.com/dosquad/go-cliversion.BuildVersion=${BUILD_VERSION} -s -w"
)

export CGO_ENABLED=1

## Build Plugins
for plugin in plugins/*; do
    if [ -d "$plugin" ]; then
        plugin_name=$(basename "$plugin")
        echo "Building plugin: $plugin_name"
        go build "${BUILD_ARGS[@]}" -o "artifacts/build/release/$(go env GOOS)/$(go env GOARCH)/plugins/${plugin_name}.so" -buildmode=plugin "./${plugin}"
    fi
done

## Build Server
echo "Building server..."
go build "${BUILD_ARGS[@]}" -buildmode=default -o "artifacts/build/release/$(go env GOOS)/$(go env GOARCH)/hostcheck" ./cmd/hostcheck
