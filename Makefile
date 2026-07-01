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

.PHONY: build test vet clean checksums release-prep pin-checksums release

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

vet:
	go vet ./...

clean:
	rm -rf $(BINDIR)

# Print copy-paste-ready SHA256 pins for install.sh's sha_* vars. Depends on
# build so the binaries exist. Detects sha256sum (Linux) or shasum (macOS).
checksums: build
	@if command -v sha256sum >/dev/null 2>&1; then sha=sha256sum; \
	elif command -v shasum >/dev/null 2>&1; then sha="shasum -a 256"; \
	else echo "checksums: need sha256sum or shasum" >&2; exit 1; fi; \
	for target in $(TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		out=$(BINDIR)/$(BINARY)-$$goos-$$goarch; \
		hash=$$($$sha "$$out" | cut -d' ' -f1); \
		echo "sha_$${goos}_$${goarch}=\"$$hash\""; \
	done

# Write the real SHA256 pins into install.sh (no hand-pasting). The script builds
# the binaries itself, edits install.sh in place, and verifies the result.
pin-checksums: build
	@scripts/pin-checksums.sh

# Local release prep: build, then auto-pin install.sh's checksums. Publishing (the
# GitHub Release + asset upload) is normally done by the tag-triggered CI workflow
# (.github/workflows/release.yml); this target just prepares a release-ready tree.
release: pin-checksums
	@echo ""; \
	echo "install.sh checksums pinned from bin/ferry-* (see docs/RELEASE.md)."; \
	echo "To publish: push a version tag and let CI build + pin + release, e.g."; \
	echo "  git commit -am 'release vX.Y.Z' && git tag vX.Y.Z && git push --follow-tags"; \
	echo "Or, to publish manually, create a GitHub Release for the tag and upload"; \
	echo "the four bin/ferry-* assets yourself."

# Build + checksums, then remind the maintainer of the manual release steps.
release-prep: checksums
	@echo ""; \
	echo "Next steps (see docs/RELEASE.md):"; \
	echo "  1. Create a GitHub Release (tag vX.Y.Z), upload the four bin/ferry-* assets."; \
	echo "  2. Paste the sha_* lines above into install.sh (replace the empty pins)."; \
	echo "  3. Commit + push; the curl | bash install path then verifies + installs."
