# The toolkit's version: the constant in internal/version/version.go, the one
# place it is written. The binary reports it, blueprints' requires_toolkit is
# checked against it, and the release job tags whatever value reaches main.
# Between releases develop holds the next release's -dev (Eg: 0.3.0-dev), so
# the release a bump target counts from is the newest final vX.Y.Z tag.
#
# Tags are ordered by their numbers alone, and pre-release tags (Eg:
# v0.3.0-rc.1) never count as a release: git's own version sort puts
# v0.3.0-rc.1 above v0.3.0. LASTRELEASE is the newest final release, and
# PRERELEASED the newest release a pre-release tag was cut for, whether or
# not that release has been cut since.
VERSION ?=
VERSION_FILE := internal/version/version.go
SEMVER_SORT := sort -t. -k1,1n -k2,2n -k3,3n
LASTRELEASE := $(shell git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | sed 's/^v//' | $(SEMVER_SORT) | tail -1)
PRERELEASED := $(shell git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-' | sed -E 's/^v([0-9]+\.[0-9]+\.[0-9]+)-.*/\1/' | sort -u | $(SEMVER_SORT) | tail -1)
# kacl-cli, the changelog tool CI runs, at the version CI pins.
KACL ?= uvx --from python-kacl==0.7.0 kacl-cli

.PHONY: bump bump-major bump-minor bump-patch dev release-binaries

# Prepare a release: set the version constant, move the [Unreleased] bullets
# in CHANGELOG.md into a dated section for it, and commit both. The release
# itself is the pull request into main, so the same review that gates
# feature work gates it.
#
#   make bump VERSION=0.2.1
#   make bump VERSION=0.3.0-rc.1   a pre-release, published as one
#   make bump-patch     last release 0.2.0 -> 0.2.1
#   make bump-minor     last release 0.2.0 -> 0.3.0
#   make bump-major     last release 0.2.0 -> 1.0.0
#
# The branch this runs on does not matter. What is released is whatever
# version reaches main, read from internal/version/version.go there, so the
# commit only has to arrive through the usual pull requests.
#
# The changelog is cut here, not by the release job, because main takes pull
# requests only: the job cannot commit there. kacl-cli writes the compare
# links from the origin remote, so an ssh remote is rewritten to the https
# form the existing links use. The commit starts with kacl-skip: because it
# empties [Unreleased], which changelog.yml would otherwise refuse on develop.
#
# A release whose pre-release was cut with every change in it has an empty
# [Unreleased]: when a v<VERSION>-* tag exists, bump records the release as
# that pre-release's, unchanged, rather than refusing it.
#
# Nothing here tags. Merging the pull request into main is what releases:
# .github/workflows/release.yml sees a version on main with no tag, tags it
# and publishes the GitHub Release. Tagging locally would put the tag on a
# commit no review has passed yet, and a tag on the wrong commit is the
# painful part to undo.
bump:
	@if [ -z "$(VERSION)" ]; then echo "usage: make bump VERSION=x.y.z or x.y.z-rc.N"; exit 1; fi
	@echo "$(VERSION)" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$$' || { echo "VERSION must look like x.y.z or x.y.z-<pre-release> (Eg: 0.3.0-rc.1)"; exit 1; }
	@if echo "$(VERSION)" | grep -Eq -- '-dev(\.|$$)'; then echo "$(VERSION) is develop's version, never a release; make dev sets it"; exit 1; fi
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit first"; exit 1; }
	@if git rev-parse -q --verify "refs/tags/v$(VERSION)" >/dev/null; then echo "tag v$(VERSION) already exists"; exit 1; fi
	@if ! $(KACL) get unreleased | grep -q '^- '; then \
		rc=$$(git tag --list 'v$(VERSION)-*' | sed 's/^v//' | sort -V | tail -1); \
		case "$(VERSION)" in *-*) rc= ;; esac; \
		if [ -z "$$rc" ]; then echo "[Unreleased] in CHANGELOG.md has no bullets; there is nothing to release"; exit 1; fi; \
		$(KACL) add changed "The release of \`$$rc\`, unchanged since it." --modify; \
	fi
	@sed -i.relbak -E 's/^(const Version = ")[^"]*(")/\1$(VERSION)\2/' $(VERSION_FILE) && rm -f $(VERSION_FILE).relbak
	@grep -q '^const Version = "$(VERSION)"' $(VERSION_FILE) || { echo "failed to update Version in $(VERSION_FILE)"; exit 1; }
	@gofmt -l $(VERSION_FILE) | grep -q . && { echo "$(VERSION_FILE) is not gofmt-clean after the edit"; exit 1; } || true
	$(KACL) release "$(VERSION)" --modify --auto-link
	@sed -i.relbak -E 's#^(\[[^]]+\]: )git@github\.com:#\1https://github.com/#' CHANGELOG.md && rm -f CHANGELOG.md.relbak
	$(KACL) verify
	git add $(VERSION_FILE) CHANGELOG.md
	git commit -m "kacl-skip: release $(VERSION)"
	@echo
	@echo "Committed. Release it with:"
	@echo "    git push -u origin HEAD"
	@echo "    gh pr create --base develop --title 'Release $(VERSION)'"
	@echo "then, once that merges:"
	@echo "    gh pr create --base main --head develop --title 'Release $(VERSION) to main'"
	@echo
	@echo "Merge the pull request into main with a merge commit, not a squash, so"
	@echo "main stays an ancestor of develop. Merging it publishes v$(VERSION)."
	@echo
	@echo "Put anything an operator must do (an apply, a configuration edit, a"
	@echo "change to an existing deployment) in the body of the pull request into"
	@echo "main: it becomes the top of the release notes."

# Derive the next version from the newest final release tag, then hand off
# to bump so its tag, changelog and working tree checks all still apply.
# The constant on develop is a -dev version, which names no release to
# count from, and a pre-release tag is no release either.
bump-major:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1+1".0.0"}')

bump-minor:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1"."$$2+1".0"}')

bump-patch:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1"."$$2"."$$3+1}')

# After a release or a pre-release is tagged, move develop to the next
# -dev, so no build of develop reports a release it is not. After a
# pre-release whose release is not cut yet, that is the release's -dev,
# which orders below its pre-releases; otherwise it is the next minor after
# the newest final release. VERSION=x.y.z names another. The commit starts
# with kacl-skip: because it changes nothing an operator would notice.
#
#   make dev                  last release 0.3.0 -> 0.4.0-dev
#   make dev                  after v0.3.0-rc.1, before v0.3.0 -> 0.3.0-dev
#   make dev VERSION=0.3.1    -> 0.3.1-dev
dev:
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit first"; exit 1; }
	@test -n "$(LASTRELEASE)$(PRERELEASED)" || { echo "no vX.Y.Z tag here; fetch the tags first"; exit 1; }
	@next="$(VERSION)"; last="$(LASTRELEASE)"; pre="$(PRERELEASED)"; \
	if [ -z "$$next" ] && [ -n "$$pre" ] && ! git rev-parse -q --verify "refs/tags/v$$pre" >/dev/null && \
		[ "$$(printf '%s\n%s\n' "$${last:-0.0.0}" "$$pre" | $(SEMVER_SORT) | tail -1)" = "$$pre" ]; then next="$$pre"; fi; \
	if [ -z "$$next" ]; then next=$$(echo "$$last" | awk -F. '{print $$1"."$$2+1".0"}'); fi; \
	echo "$$next" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION must look like x.y.z"; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/v$$next" >/dev/null; then echo "v$$next is released already; develop moves past it"; exit 1; fi; \
	sed -i.relbak -E "s/^(const Version = \")[^\"]*(\")/\1$$next-dev\2/" $(VERSION_FILE) && rm -f $(VERSION_FILE).relbak; \
	grep -q "^const Version = \"$$next-dev\"" $(VERSION_FILE) || { echo "failed to update Version in $(VERSION_FILE)"; exit 1; }; \
	git add $(VERSION_FILE); \
	git commit -m "kacl-skip: develop is $$next-dev"; \
	echo; \
	echo "Committed. Open it as a pull request into develop:"; \
	echo "    git push -u origin HEAD"; \
	echo "    gh pr create --base develop --title 'Develop is $$next-dev'"

# The Linux binaries a release attaches, with a SHA256SUMS file beside them,
# in dist/. A host owner runs `paisans site admit` on the host itself and
# needs the CLI and nothing else, so each binary is static (CGO_ENABLED=0)
# and runs on any Linux of its architecture with no Go and no C library.
# .github/workflows/release.yml runs this target, so a local run builds what
# a release ships.
#
# The -X flag sets the commit the binary names (see COMMIT below).
#
# dist/ is emptied first, so SHA256SUMS lists only what this run built.
# SHA256SUMS names the files without a directory, so `sha256sum -c` works in
# whatever directory they were downloaded to. -trimpath keeps the builder's
# paths out of the binary; -s -w drops the symbol table and DWARF, which a
# release binary has no use for.
RELEASE_PLATFORMS := linux/amd64 linux/arm64

# The commit a binary names in every record it writes. Go stamps it from a
# git checkout by itself; it is also set here with -X, so the binary names
# it whatever builds it, and a packager building from a source tarball,
# which has no git metadata, gives it the same way:
#
#   go build -ldflags "-X github.com/paisans-software/paisans-cli/internal/version.buildCommit=<full commit>" ./cmd/paisans
#
# COMMIT=<full commit> overrides it here.
COMMIT_VAR := github.com/paisans-software/paisans-cli/internal/version.buildCommit
COMMIT ?= $(shell git rev-parse HEAD)

release-binaries:
	rm -rf dist
	mkdir -p dist
	@for p in $(RELEASE_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building dist/paisans-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w -X $(COMMIT_VAR)=$(COMMIT)" \
			-o dist/paisans-$$os-$$arch ./cmd/paisans || exit 1; \
	done
	cd dist && shasum -a 256 paisans-* > SHA256SUMS
	@cat dist/SHA256SUMS
