#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: The Pumi Authors

# Downloads the release binaries that are pinned by version and checksum
# (decision 0086) and installs them in /usr/local/bin. A download whose
# checksum does not match stops the script, and so the image build.
#
# The checksums are copied from what each project publishes with its release:
# golangci-lint's checksum file, which has SHA-256 checksums, and the
# integrity of Claude Code's npm packages in the npm registry, which is a
# SHA-512 checksum in base64, here in hex.

set -eu

GOLANGCI_LINT_VERSION=2.15.0
GOLANGCI_LINT_SHA256_AMD64=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_LINT_SHA256_ARM64=b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998

# Claude Code, which the adapter's conformance tests run (decision 0244). The
# npm package @anthropic-ai/claude-code-linux-<arch> holds the native claude
# for Linux with glibc, which the golang image has.
CLAUDE_CODE_VERSION=2.1.332
CLAUDE_CODE_SHA512_X64=7f8256873c18bad2481d2702eff75838f5fab36f2db7b6400e1b0e8bccf0387ab97c53c4b94782861d8d31f8a74a0d06ad472463169e79b0b88740ea88accb73
CLAUDE_CODE_SHA512_ARM64=0651cd12d2b7f0a04f594df5178954492f6c1dc942c9f59b4e14f5084aefc711fcf977258d82d9950c1dc970644c3eb6af0d5381f135bb56beaae3164b435226

# fetch downloads the archive at $1, checks it against the checksum in $2 with
# the program in $4, sha256sum or sha512sum, and copies the file at path $3
# inside the archive to /usr/local/bin.
fetch() {
	dir=$(mktemp -d)
	archive=$dir/$(basename "$1")
	curl --fail --silent --show-error --location --output "$archive" "$1"
	echo "$2  $archive" | "$4" --check --strict
	tar --extract --gzip --file "$archive" --directory "$dir" "$3"
	install -m 0755 "$dir/$3" /usr/local/bin/
	rm -rf "$dir"
}

case "$(dpkg --print-architecture)" in
amd64)
	golangci_lint_arch=amd64
	golangci_lint_sha256=$GOLANGCI_LINT_SHA256_AMD64
	claude_arch=x64
	claude_sha512=$CLAUDE_CODE_SHA512_X64
	;;
arm64)
	golangci_lint_arch=arm64
	golangci_lint_sha256=$GOLANGCI_LINT_SHA256_ARM64
	claude_arch=arm64
	claude_sha512=$CLAUDE_CODE_SHA512_ARM64
	;;
*)
	echo "install-tools.sh: unsupported architecture $(dpkg --print-architecture)" >&2
	exit 1
	;;
esac

golangci_lint=golangci-lint-$GOLANGCI_LINT_VERSION-linux-$golangci_lint_arch
fetch \
	"https://github.com/golangci/golangci-lint/releases/download/v$GOLANGCI_LINT_VERSION/$golangci_lint.tar.gz" \
	"$golangci_lint_sha256" \
	"$golangci_lint/golangci-lint" \
	sha256sum

fetch \
	"https://registry.npmjs.org/@anthropic-ai/claude-code-linux-$claude_arch/-/claude-code-linux-$claude_arch-$CLAUDE_CODE_VERSION.tgz" \
	"$claude_sha512" \
	package/claude \
	sha512sum
