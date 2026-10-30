GO ?= go

.PHONY: build plugin test check integration
build:
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -o bin/k8nnect ./cmd/k8nnect
plugin: build
	ln -sf k8nnect bin/kubectl-k8nnect
test:
	$(GO) test -race ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
integration: build
	bash test/integration/linux.sh

.PHONY: site
site:
	python3 scripts/build_site.py
	python3 scripts/check_site.py
