# Version reported by the binary: an explicit VERSION=, or the constant in
# app.go. The constant is the source of truth rather than `git describe`,
# because this repository carries every upstream tag, so a description here
# names an upstream release the build may have nothing to do with. The
# binary appends "+wisp" itself, so what is passed is the bare number.
VERSION ?=
DEFAULTVER := $(shell sed -n 's/^[[:space:]]*softwareVer = "\(.*\)"/\1/p' app.go)
VERSTR := $(if $(VERSION),$(VERSION),$(DEFAULTVER))
VERFLAG := $(if $(VERSTR),-X 'github.com/writefreely/writefreely.softwareVer=$(VERSTR)',)

# Release archives are named after the version.
ARCHIVEVER := $(VERSTR)

LDFLAGS=-ldflags="-s -w $(VERFLAG) -extldflags '-static'"
BASELDFLAGS=-ldflags="-s -w $(VERFLAG)"

GOCMD=go
GOINSTALL=$(GOCMD) install $(LDFLAGS)
GOBUILD=$(GOCMD) build $(LDFLAGS)
GOTEST=$(GOCMD) test $(LDFLAGS)
GOGET=$(GOCMD) get
BINARY_NAME=writefreely
BUILDPATH=build/$(BINARY_NAME)
DOCKERCMD=docker
IMAGE_NAME=ghcr.io/josephquigley/writefreely-wisp
TMPBIN=./tmp

all : build

ci: deps
	cd cmd/writefreely; $(GOBUILD) -v

build: deps
	cd cmd/writefreely; $(GOBUILD) -v -tags='netgo sqlite'

build-no-sqlite: deps-no-sqlite
	cd cmd/writefreely; $(GOBUILD) -v -tags='netgo' -o $(BINARY_NAME)

build-linux: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=linux/amd64, -dest build/ $(LDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-windows: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=windows/amd64, -dest build/ $(LDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-darwin: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=darwin/amd64, -dest build/ $(BASELDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-darwin-arm64: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=darwin/arm64, -dest build/ $(BASELDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-arm6: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=linux/arm-6, -dest build/ $(LDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-arm7: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=linux/arm-7, -dest build/ $(LDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-arm64: deps
	@hash xgo > /dev/null 2>&1; if [ $$? -ne 0 ]; then \
		$(GOCMD) install src.techknowlogick.com/xgo@latest; \
	fi
	xgo --targets=linux/arm64, -dest build/ $(LDFLAGS) -tags='netgo sqlite' -go go-1.25.x -out writefreely -pkg ./cmd/writefreely .

build-docker :
	$(DOCKERCMD) build --build-arg WRITEFREELY_VERSION=$(VERSTR) -t $(IMAGE_NAME):latest $(if $(VERSTR),-t $(IMAGE_NAME):$(VERSTR),) .

# Prepare a release: bump the version compiled into the binary and commit
# it. The release itself is the pull request into main, so the same CI that
# gates feature work gates it.
#
#   make bump VERSION=0.18.1
#   make bump-patch     0.17.2 -> 0.17.3
#   make bump-minor     0.17.2 -> 0.18.0
#   make bump-major     0.17.2 -> 1.0.0
#
# Named bump rather than release because release already builds the
# cross-compiled binary tarballs.
#
# The branch this runs on does not matter. What is released is whatever
# version reaches main, read from app.go there, so the commit only has to
# arrive through the usual pull request.
#
# Nothing here tags. Merging the pull request into main is what releases:
# .github/workflows/wisp-release.yml sees a version on main with no tag,
# tags it, publishes the GitHub Release and retags the image that was
# already built. Tagging locally would put the tag on a commit CI has not
# passed yet, and a tag on the wrong commit is the painful part to undo.
bump:
	@if [ -z "$(VERSION)" ]; then echo "usage: make bump VERSION=x.y.z"; exit 1; fi
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION must look like x.y.z"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit or stash first"; exit 1; }
	@if git rev-parse -q --verify "refs/tags/v$(VERSION)+wisp" >/dev/null; then echo "tag v$(VERSION)+wisp already exists"; exit 1; fi
	@sed -i.relbak -E 's/^([[:space:]]*softwareVer = ")[^"]*(")/\1$(VERSION)\2/' app.go && rm -f app.go.relbak
	@grep -q 'softwareVer = "$(VERSION)"' app.go || { echo "failed to update softwareVer in app.go"; exit 1; }
	@gofmt -l app.go | grep -q . && { echo "app.go is not gofmt-clean after the edit"; exit 1; } || true
	git add app.go
	git commit -m "Release $(VERSION)"
	@echo
	@echo "Committed. Release it with:"
	@echo "    git push"
	@echo "    gh pr create --base main --title 'Release $(VERSION)'"
	@echo
	@echo "The pull request into main needs one bullet under [Unreleased] in"
	@echo "CHANGELOG.md: one sentence, 35 words at most, summarising the release."
	@echo "Merging moves it into a section for $(VERSION)+wisp."
	@echo
	@echo "Merging it publishes v$(VERSION)+wisp. Put anything an operator"
	@echo "must do (migrations, new config keys) in the pull request body:"
	@echo "it becomes the top of the release notes."

# Derive the next version from the constant bump itself maintains, then
# hand off to bump so its branch, tag and working tree checks all still
# apply. app.go is the source of truth rather than `git describe`, because
# this repository carries every upstream tag and a description names an
# upstream release rather than a release of this fork.
bump-major:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1+1".0.0"}')

bump-minor:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1"."$$2+1".0"}')

bump-patch:
	@"$(MAKE)" bump VERSION=$$(echo "$(DEFAULTVER)" | awk -F. '{print $$1"."$$2"."$$3+1}')

test:
	$(GOTEST) -v ./...

# Run the test suite against a throwaway Postgres in local Docker. See
# docs/postgres-testing.md for what the variables mean.
#
#   make test-postgres
#   make test-postgres GOTESTFLAGS='-run TestFoo -v'
#   make test-postgres PG_IMAGE=postgres:18.1
#
# The container gets a random loopback port, so it cannot collide with a
# Postgres already running on 5432, and is removed on every exit path,
# including a failed test run and Ctrl-C. It is local Docker only; nothing
# here takes a DOCKER_HOST or a remote context into account, so do not point
# one at a server you care about.
#
# GOTAGS defaults to sqlite, as in CI: many app-level tests (signup, OAuth
# state, settings, db copy) sit behind that build tag, and without it they are
# not compiled at all, so a green run would silently skip them. It is kept
# apart from GOTESTFLAGS so that overriding the flags cannot drop the tag;
# GOTAGS= runs without it.
PG_IMAGE ?= postgres:18
GOTAGS ?= sqlite
GOTESTFLAGS ?=
test-postgres:
	@set -eu; \
	name="wf-test-pg-$$$$"; \
	cleanup() { echo "test-postgres: removing $$name"; $(DOCKERCMD) rm -f "$$name" >/dev/null 2>&1 || true; }; \
	trap cleanup EXIT; trap 'exit 130' INT TERM; \
	echo "test-postgres: starting $$name ($(PG_IMAGE))"; \
	$(DOCKERCMD) run -d --rm --name "$$name" \
		-e POSTGRES_USER=writefreely -e POSTGRES_PASSWORD=writefreely -e POSTGRES_DB=writefreely \
		-p 127.0.0.1::5432 --tmpfs /var/lib/postgresql \
		"$(PG_IMAGE)" >/dev/null; \
	i=0; until $(DOCKERCMD) exec "$$name" pg_isready -q -h 127.0.0.1 -U writefreely -d writefreely; do \
		i=$$((i+1)); if [ $$i -ge 60 ]; then echo "test-postgres: Postgres did not become ready"; $(DOCKERCMD) logs "$$name"; exit 1; fi; \
		sleep 1; \
	done; \
	port=$$($(DOCKERCMD) port "$$name" 5432/tcp | head -n1 | sed 's/.*://'); \
	echo "test-postgres: ready on 127.0.0.1:$$port"; \
	WF_TEST_DB_TYPE=postgres \
	WF_TEST_PG_DSN="postgres://writefreely:writefreely@127.0.0.1:$$port/writefreely?sslmode=disable" \
		$(GOCMD) test -count=1 -tags '$(GOTAGS)' $(GOTESTFLAGS) ./...

# Run the test suite against a throwaway MySQL or MariaDB in local Docker,
# the twin of test-postgres. See docs/database-testing.md.
#
#   make test-mysql
#   make test-mysql MYSQL_IMAGE=mysql:8.4
#   make test-mysql GOTESTFLAGS='-run TestFoo -v'
#
# Same guarantees as test-postgres: a random loopback port, removed on every
# exit path, local Docker only, and the same GOTAGS default. The tests connect as root, which they need
# to create and drop a database per test.
MYSQL_IMAGE ?= mariadb:11
test-mysql:
	@set -eu; \
	name="wf-test-mysql-$$$$"; \
	cleanup() { echo "test-mysql: removing $$name"; $(DOCKERCMD) rm -f "$$name" >/dev/null 2>&1 || true; }; \
	trap cleanup EXIT; trap 'exit 130' INT TERM; \
	echo "test-mysql: starting $$name ($(MYSQL_IMAGE))"; \
	$(DOCKERCMD) run -d --rm --name "$$name" \
		-e MYSQL_ROOT_PASSWORD=writefreely -e MARIADB_ROOT_PASSWORD=writefreely \
		-p 127.0.0.1::3306 --tmpfs /var/lib/mysql \
		"$(MYSQL_IMAGE)" >/dev/null; \
	i=0; until $(DOCKERCMD) exec "$$name" sh -c \
		'mariadb-admin ping -h 127.0.0.1 -uroot -pwritefreely --silent 2>/dev/null || mysqladmin ping -h 127.0.0.1 -uroot -pwritefreely --silent 2>/dev/null' >/dev/null; do \
		i=$$((i+1)); if [ $$i -ge 120 ]; then echo "test-mysql: MySQL did not become ready"; $(DOCKERCMD) logs "$$name"; exit 1; fi; \
		sleep 1; \
	done; \
	port=$$($(DOCKERCMD) port "$$name" 3306/tcp | head -n1 | sed 's/.*://'); \
	echo "test-mysql: ready on 127.0.0.1:$$port"; \
	WF_TEST_DB_TYPE=mysql \
	WF_TEST_MYSQL_DSN="root:writefreely@tcp(127.0.0.1:$$port)/" \
		$(GOCMD) test -count=1 -tags '$(GOTAGS)' $(GOTESTFLAGS) ./...

run:
	$(GOINSTALL) -tags='netgo sqlite' ./...
	$(BINARY_NAME) --debug

deps :
	$(GOGET) -tags='sqlite' -d -v ./...

deps-no-sqlite:
	$(GOGET) -d -v ./...

install : build
	cmd/writefreely/$(BINARY_NAME) --config
	cmd/writefreely/$(BINARY_NAME) --gen-keys
	cmd/writefreely/$(BINARY_NAME) --init-db
	cd less/; $(MAKE) install $(MFLAGS)

release : clean ui
	mkdir -p $(BUILDPATH)
	rsync -av --exclude=".*" templates $(BUILDPATH)
	rsync -av --exclude=".*" pages $(BUILDPATH)
	rsync -av --exclude=".*" static $(BUILDPATH)
	rm -r $(BUILDPATH)/static/local
	scripts/invalidate-css.sh $(BUILDPATH)
	mkdir $(BUILDPATH)/keys
	$(MAKE) build-linux
	mv build/$(BINARY_NAME)-linux-amd64 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_linux_amd64.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-arm6
	mv build/$(BINARY_NAME)-linux-arm-6 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_linux_arm6.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-arm7
	mv build/$(BINARY_NAME)-linux-arm-7 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_linux_arm7.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-arm64
	mv build/$(BINARY_NAME)-linux-arm64 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_linux_arm64.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-darwin
	mv build/$(BINARY_NAME)-darwin-10.12-amd64 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_macos_amd64.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-darwin-arm64
	mv build/$(BINARY_NAME)-darwin-10.12-arm64 $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_macos_arm64.tar.gz -C build $(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME)
	$(MAKE) build-windows
	mv build/$(BINARY_NAME)-windows-4.0-amd64.exe $(BUILDPATH)/$(BINARY_NAME).exe
	cd build; zip -r ../$(BINARY_NAME)_$(ARCHIVEVER)_windows_amd64.zip ./$(BINARY_NAME)
	rm $(BUILDPATH)/$(BINARY_NAME).exe

# This assumes you're on linux/amd64
release-linux : clean ui
	mkdir -p $(BUILDPATH)
	cp -r templates $(BUILDPATH)
	cp -r pages $(BUILDPATH)
	cp -r static $(BUILDPATH)
	mkdir $(BUILDPATH)/keys
	$(MAKE) build-no-sqlite
	mv cmd/writefreely/$(BINARY_NAME) $(BUILDPATH)/$(BINARY_NAME)
	tar -cvzf $(BINARY_NAME)_$(ARCHIVEVER)_linux_amd64.tar.gz -C build $(BINARY_NAME)

release-docker :
	$(DOCKERCMD) push $(IMAGE_NAME)

ui : force_look
	cd less/; $(MAKE) $(MFLAGS)
	cd prose/; $(MAKE) $(MFLAGS)

$(TMPBIN):
	mkdir -p $(TMPBIN)

$(TMPBIN)/xgo: deps $(TMPBIN)
	$(GOBUILD) -o $(TMPBIN)/xgo src.techknowlogick.com/xgo

clean :
	-rm -rf build
	-rm -rf tmp
	cd less/; $(MAKE) clean $(MFLAGS)

force_look :
	true
