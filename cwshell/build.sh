#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="${1:-build}"
SDKROOT="${SDKROOT:-/home/noel/.swiftpm/swift-sdks/darwin.artifactbundle/Developer/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS.sdk}"

if [ ! -d "${SDKROOT}" ]; then
    echo "error: iOS SDK not found at ${SDKROOT}" >&2
    echo "  hint: run 'xtool sdk install <Xcode.xip>' first" >&2
    exit 1
fi

export CGO_ENABLED=1
export GOOS=ios
export GOARCH=arm64
export GOWORK=off
export CC="clang -target arm64-apple-ios17.0 -isysroot ${SDKROOT}"
export CGO_CFLAGS="-isysroot ${SDKROOT} -target arm64-apple-ios17.0"
export CGO_LDFLAGS="-isysroot ${SDKROOT} -target arm64-apple-ios17.0"

mkdir -p "${OUTPUT_DIR}"
go build -tags ios -buildmode=c-archive -o "${OUTPUT_DIR}/libcwshell.a" .

echo "Built: ${OUTPUT_DIR}/libcwshell.a"
echo "Header: ${OUTPUT_DIR}/libcwshell.h"
ls -lh "${OUTPUT_DIR}/libcwshell.a"
