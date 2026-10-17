# The toolkit's version: the constant in internal/version/version.go, the one
# place it is written. The binary reports it, blueprints' requires_toolkit is
# checked against it, and the release job tags whatever value reaches main.
VERSION ?=
VERSION_FILE := internal/version/version.go
DEFAULTVER := $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' $(VERSION_FILE))
# kacl-cli, the changelog tool CI runs, at the version CI pins.
KACL ?= uvx --from python-kacl==0.7.0 kacl-cli

.PHONY: bump bump-major bump-minor bump-patch release-binaries

# Prepare a release: set the version constant, move the [Unreleased] bullets
# in CHANGELOG.md into a dated section for it, and commit both. The release
# itself is the pull request into main, so the same review that gates
# feature work gates it.
#
#   make bump VERSION=0.2.1
#   make bump-patch     0.2.0 -> 0.2.1
#   make bump-minor     0.2.0 -> 0.3.0
#   make bump-major     0.2.0 -> 1.0.0
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

# Derive the next version from the constant bump itself maintains, then hand
# off to bump so its tag, changelog and working tree checks all still apply.
bump-major:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1+1".0.0"}')

bump-minor:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1"."$$2+1".0"}')

bump-patch:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1"."$$2"."$$3+1}')

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
