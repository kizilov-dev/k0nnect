GO ?= go

IMAGE ?= k0nnect:dev

.PHONY: build plugin test check integration image manifest
build:
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -o bin/k0nnect ./cmd/k0nnect
plugin: build
	ln -sf k0nnect bin/kubectl-k0nnect
test:
	$(GO) test -race ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
integration: build
	bash test/integration/linux.sh
image:
	docker build -t $(IMAGE) .
manifest:
	@$(GO) run ./cmd/k0nnect manifest --image $(IMAGE)

.PHONY: site
site:
	python3 scripts/build_site.py
	python3 scripts/check_site.py
