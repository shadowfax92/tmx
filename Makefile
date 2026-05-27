PREFIX ?= $(HOME)/bin
VERSION ?= 0.1.0

build:
	go build -ldflags "-X tmx/cmd.Version=$(VERSION)" -o tmx .

install: build
	cp tmx $(PREFIX)/tmx
	codesign --force --sign - $(PREFIX)/tmx

uninstall:
	rm -f $(PREFIX)/tmx

test:
	go test ./...

clean:
	rm -f tmx
