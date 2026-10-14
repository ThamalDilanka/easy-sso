GO ?= go
PYTHON ?= python3
VERSION ?= 0.1.0-dev
COMMIT ?= unreleased
LDFLAGS = -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test check fmt crossbuild package demo
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/easysso ./cmd/easysso
demo:
	$(PYTHON) -c "import pathlib, webbrowser; webbrowser.open(pathlib.Path('docs/demo/easy-sso-demo.html').resolve().as_uri())"
test:
	$(GO) test -race -count=1 ./...
	$(PYTHON) -m unittest discover -s tools -p 'test_*.py'
fmt:
	gofmt -w cmd internal spec
check:
	@test -z "$$(gofmt -l cmd internal spec)" || { gofmt -l cmd internal spec; exit 1; }
	$(GO) vet ./...
	$(GO) mod verify
	$(PYTHON) tools/check-public.py
crossbuild:
	GO=$(GO) $(PYTHON) tools/package.py --build-only --version '$(VERSION)' --commit '$(COMMIT)'
package:
	GO=$(GO) $(PYTHON) tools/package.py --version '$(VERSION)' --commit '$(COMMIT)'
