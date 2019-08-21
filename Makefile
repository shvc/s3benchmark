BINARY=s3benchmark
BUILDDATE=$(shell date +'%Y-%m-%dT%H:%M:%SZ')
VERSION=2.1.$(shell git rev-list HEAD --count)
LONGVER=${VERSION}@${BUILDDATE}

LDFLAGS=-ldflags "-X main.version=${LONGVER}"

.DEFAULT_GOAL:=default
pkg:
	@echo "Building Linux amd64 ${BINARY}-${VERSION}"
	GOOS=linux GOARCH=amd64 go build ${LDFLAGS}
	zip -m ${BINARY}-${VERSION}-linux.zip ${BINARY}
	
	@echo "Building Macos amd64 ${BINARY}-${VERSION}"
	GOOS=darwin GOARCH=amd64 go build ${LDFLAGS}
	zip -m ${BINARY}-${VERSION}-macos.zip ${BINARY}
	
	@echo "Building Windows amd64 ${BINARY}-${VERSION}"
	GOOS=windows GOARCH=amd64 go build ${LDFLAGS}
	zip -m ${BINARY}-${VERSION}-win.zip ${BINARY}.exe

test:
	go test

default:
	@echo "Building ${BINARY}-${VERSION}"
	go build ${LDFLAGS}

install: default
	install ${BINARY} /usr/local/bin/

clean:
	rm -rf *zip
	rm -rf ${BINARY}

.PHONY: pkg test default clean
