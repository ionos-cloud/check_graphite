NAME         ?= monzero
PKGNAME      ?= git.zero-knowledge.org/gibheer/monzero

export GO111MODULE=on

# build specific flags
DESTDIR      ?= .
prefix       ?= /usr/local
exec_prefix  ?= ${prefix}
bindir       ?= ${exec_prefix}/bin
sysconfdir   ?= ${prefix}/etc/${NAME}
datarootdir  ?= ${prefix}/share
man1dir      ?= ${datarootdir}/man/man1
datadir      ?= ${datarootdir}/${NAME}
WRKDIR       ?= build
GOBIN        ?= go

# set GOOS to linux by default
GOOS         ?= linux
BUILDID      = 0x`head -c20 /dev/urandom | od -An -tx | tr -d ' \n'`
LDFLAGS      += -B ${BUILDID}
BUILD_DATE   ?= `date +%FT%T%z`
LDFLAGS      += -X main.BUILD_DATE=${BUILD_DATE}

MONFRONT_FILES = $(wildcard cmd/monfront/*.go) $(wildcard *.go)

all: build

build: env/${WRKDIR} moncheck monwork monfront

env/${WRKDIR}:
	mkdir -p ${WRKDIR}

moncheck:
	GOOS=${GOOS} CGO_ENABLED=false go build -ldflags="${LDFLAGS}" -o ${WRKDIR}/moncheck ${PKGNAME}/cmd/moncheck

monwork:
	GOOS=${GOOS} CGO_ENABLED=false go build -ldflags="${LDFLAGS}" -o ${WRKDIR}/monwork ${PKGNAME}/cmd/monwork

monfront:
	GOOS=${GOOS} CGO_ENABLED=false go build -ldflags="${LDFLAGS}" -o ${WRKDIR}/monfront ${PKGNAME}/cmd/monfront

clean:
	-rm -r ${WRKDIR}

install: build preinstall install-monwork install-moncheck install-monfront

preinstall:
	install -d -m 0755 ${DESTDIR}${bindir}
	install -d -m 0755 ${DESTDIR}${sysconfdir}

install-moncheck: preinstall
	install -m 0755 ${WRKDIR}/moncheck ${DESTDIR}${bindir}
	install -m 0644 moncheck.conf.example ${DESTDIR}${sysconfdir}
	install -m 0644 man/moncheck.1 ${DESTDIR}${man1dir}

install-monwork: preinstall
	install -m 0755 ${WRKDIR}/monwork ${DESTDIR}${bindir}
	install -m 0644 monwork.conf.example ${DESTDIR}${sysconfdir}

install-monfront: preinstall
	install -m 0755 ${WRKDIR}/monfront ${DESTDIR}${bindir}
	install -m 0644 monfront.conf.example ${DESTDIR}${sysconfdir}
	install -d -m 0755 ${DESTDIR}${datadir}/templates
	sed -i'' "s-\#template_path.*-template_path = \"${datadir}/templates\"-g" ${DESTDIR}${sysconfdir}/monfront.conf.example
	find cmd/monfront/templates -type f -exec install -m 0644 "{}" ${DESTDIR}${datadir}/templates \;

package: DESTDIR = ${NAME}-${VERSION}
package: install
	tar -czf ${NAME}-${VERSION}.tar.gz ${DESTDIR}
	rm -R ${DESTDIR}

.PHONY: clean build moncheck monwork monfront
