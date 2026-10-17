# The toolkit's version: the constant in internal/version/version.go, the one
# place it is written. The binary reports it, blueprints' requires_toolkit is
# checked against it, and the release job tags whatever value reaches main.
# Between releases develop holds the next minor's -dev (Eg: 0.3.0-dev), so
# the release a bump target counts from is the newest vX.Y.Z tag.
VERSION ?=
VERSION_FILE := internal/version/version.go
LASTRELEASE := $(shell git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -1 | sed 's/^v//')
# kacl-cli, the changelog tool CI runs, at the version CI pins.
KACL ?= uvx --from python-kacl==0.7.0 kacl-cli

.PHONY: bump bump-major bump-minor bump-patch dev release-binaries

# Prepare a release: set the version constant, move the [Unreleased] bullets
# in CHANGELOG.md into a dated section for it, and commit both. The release
# itself is the pull request into main, so the same review that gates
# feature work gates it.
#
#   make bump VERSION=0.2.1
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
# Nothing here tags. Merging the pull request into main is what releases:
# .github/workflows/release.yml sees a version on main with no tag, tags it
# and publishes the GitHub Release. Tagging locally would put the tag on a
# commit no review has passed yet, and a tag on the wrong commit is the
# painful part to undo.
bump:
	@if [ -z "$(VERSION)" ]; then echo "usage: make bump VERSION=x.y.z"; exit 1; fi
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION must look like x.y.z"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit first"; exit 1; }
	@if git rev-parse -q --verify "refs/tags/v$(VERSION)" >/dev/null; then echo "tag v$(VERSION) already exists"; exit 1; fi
	@$(KACL) get unreleased | grep -q '^- ' || { echo "[Unreleased] in CHANGELOG.md has no bullets; there is nothing to release"; exit 1; }
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

# Derive the next version from the newest release tag, then hand off to
# bump so its tag, changelog and working tree checks all still apply. The
# constant on develop is a -dev version, which names no release to count
# from.
bump-major:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1+1".0.0"}')

bump-minor:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1"."$$2+1".0"}')

bump-patch:
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags, or give make bump VERSION=x.y.z"; exit 1; }
	@"$(MAKE)" bump VERSION=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1"."$$2"."$$3+1}')

# After a release is tagged, move develop to the next pre-release, so no
# build of develop reports a release it is not. The default is the next
# minor after the newest release tag, as -dev; VERSION=x.y.z names another.
# The commit starts with kacl-skip: because it changes nothing an operator
# would notice.
#
#   make dev                  last release 0.3.0 -> 0.4.0-dev
#   make dev VERSION=0.3.1    -> 0.3.1-dev
dev:
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit first"; exit 1; }
	@test -n "$(LASTRELEASE)" || { echo "no vX.Y.Z tag here; fetch the tags first"; exit 1; }
	@next="$(VERSION)"; \
	if [ -z "$$next" ]; then next=$$(echo "$(LASTRELEASE)" | awk -F. '{print $$1"."$$2+1".0"}'); fi; \
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
# dist/ is emptied first, so SHA256SUMS lists only what this run built.
# SHA256SUMS names the files without a directory, so `sha256sum -c` works in
# whatever directory they were downloaded to. -trimpath keeps the builder's
# paths out of the binary; -s -w drops the symbol table and DWARF, which a
# release binary has no use for.
RELEASE_PLATFORMS := linux/amd64 linux/arm64

release-binaries:
	rm -rf dist
	mkdir -p dist
	@for p in $(RELEASE_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building dist/paisans-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" \
			-o dist/paisans-$$os-$$arch ./cmd/paisans || exit 1; \
	done
	cd dist && shasum -a 256 paisans-* > SHA256SUMS
	@cat dist/SHA256SUMS
