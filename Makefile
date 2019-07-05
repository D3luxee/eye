# vim: set ft=make ffs=unix fenc=utf8:
# vim: set noet ts=4 sw=4 tw=72 list:
#
EYEVERSION != cat `git rev-parse --show-toplevel`/VERSION
BRANCH != git rev-parse --symbolic-full-name --abbrev-ref HEAD
GITHASH != git rev-parse --short HEAD
VERSIONSTRING = "$(EYEVERSION)-$(GITHASH)/$(BRANCH)"

all: validate

validate:
	@go build ./...
	@go vet ./...
	@golint ./...
	@ineffassign ./cmd/eye
	@ineffassign ./internal/eye
	@ineffassign ./internal/eye.mock
	@ineffassign ./internal/eye.msg
	@ineffassign ./internal/eye.rest
	@ineffassign ./internal/eye.stmt
	@ineffassign ./lib/eye.proto
	@ineffassign ./lib/eye.wall
	@codecoroner funcs ./...

release: man
	
man: install_freebsd install_linux
	@${GOPATH}/bin/eye --create-manpage > docs/man/eye.1

install_freebsd: generate
	@env GOOS=freebsd GOARCH=amd64 go install -ldflags "-X main.eyeVersion=$(VERSIONSTRING)" ./...

install_linux: generate
	@env GOOS=linux GOARCH=amd64 go install -ldflags "-X main.eyeVersion=$(VERSIONSTRING)" ./...

generate:
	@go generate ./cmd/...
