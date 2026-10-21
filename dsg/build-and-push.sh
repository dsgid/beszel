#!/usr/bin/env bash
# Builds the hub image with upstream's internal/dockerfile_hub and pushes it to registry.dsg.id.
# Needs a Docker host logged in to registry.dsg.id (dsg-fringe as devops). Prints the pushed reference.
set -euo pipefail

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
if [ -n "$(git -C "$root" status --porcelain)" ]; then
	echo "refusing to build from a dirty tree" >&2
	exit 1
fi

version=$(sed -n 's/^\s*Version = "\(.*\)"/\1/p' "$root/beszel.go")
if [ -z "$version" ]; then
	echo "could not read Version from beszel.go" >&2
	exit 1
fi
tag="registry.dsg.id/devops/beszel:${version}-dsg-$(git -C "$root" rev-parse --short=12 HEAD)"

# build from an export of HEAD so the image holds only committed files plus the fresh UI build
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git -C "$root" archive HEAD | tar -x -C "$work"

# same UI build as upstream CI, which runs it before docker build
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$work/internal/site:/site" -w /site \
	oven/bun:1.4.3-alpine sh -c "bun install --frozen-lockfile && bun run build"

docker build --pull -f "$work/internal/dockerfile_hub" -t "$tag" "$work"
docker push "$tag"
docker image inspect --format '{{index .RepoDigests 0}}' "$tag"
