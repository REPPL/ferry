BINARY := ferry
BINDIR := bin
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64
# Build version stamped into `ferry --version`. Defaults to the in-source dev value;
# the release workflow passes the git tag (VERSION=vX.Y.Z). SemVer, v-prefixed.
VERSION ?=
# -s -w strips the symbol table and DWARF debug info; -X stamps the version.
# -trimpath (in the build recipe) rewrites absolute source paths to module paths so
# no local filesystem path is embedded in the binary. Together these produce a
# smaller, path-clean binary suitable for public distribution.
LDFLAGS := -s -w$(if $(VERSION), -X github.com/REPPL/ferry/cmd.version=$(VERSION),)

.PHONY: build test vet clean checksums release-prep release preflight gen-docs

# Cross-compile every supported target to bin/ferry-<goos>-<arch>.
# Pass VERSION=vX.Y.Z to stamp the version (release builds); omit for a dev build.
build:
	@mkdir -p $(BINDIR)
	@for target in $(TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		out=$(BINDIR)/$(BINARY)-$$goos-$$goarch; \
		echo "building $$out"; \
		GOOS=$$goos GOARCH=$$goarch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out . || exit 1; \
	done

test:
	go test ./...

# Regenerate the committed CLI reference under docs/reference/cli from the cobra
# command tree. The generator is a standalone main package the ferry binary never
# imports; output is deterministic (no auto-gen timestamp) so CI can diff it.
gen-docs:
	go run ./tools/gendocs

vet:
	go vet ./...

# Pre-push gate (invoked by .githooks/pre-push): the same four steps the CI
# check job runs (build, vet, test, race-enabled internal tests), natively.
# Host-native `go build` — not the cross-compiling `build` target — because it
# mirrors CI, not a release.
preflight:
	go build ./...
	go vet ./...
	go test ./...
	go test -race ./internal/...

clean:
	rm -rf $(BINDIR)

# Write bin/checksums.txt: the SHA256 of every binary in sha256sum format.
# Depends on build so the binaries exist. The release workflow uploads this file
# as a release asset; install.sh fetches it from the release and verifies each
# download against it.
checksums: build
	@scripts/gen-checksums.sh

# Local release prep: build, then write bin/checksums.txt. Publishing (the GitHub
# Release + asset upload) is normally done by the tag-triggered CI workflow
# (.github/workflows/release.yml); this target just prepares a release-ready tree.
release: checksums
	@echo ""; \
	echo "bin/checksums.txt written over bin/ferry-* (see docs/how-to/cutting-a-release.md)."; \
	echo "To publish: push a version tag and let CI build + checksum + release, e.g."; \
	echo "  git commit -am 'release vX.Y.Z' && git tag vX.Y.Z && git push --follow-tags"; \
	echo "Or, to publish manually, create a GitHub Release for the tag and upload"; \
	echo "the four bin/ferry-* binaries and bin/checksums.txt yourself."

# Build + checksums, then remind the maintainer of the manual release steps.
release-prep: checksums
	@echo ""; \
	echo "Next steps (see docs/how-to/cutting-a-release.md):"; \
	echo "  1. Create a GitHub Release (tag vX.Y.Z)."; \
	echo "  2. Upload the four bin/ferry-* binaries and bin/checksums.txt as assets."; \
	echo "  3. install.sh then fetches checksums.txt from the release and verifies."
