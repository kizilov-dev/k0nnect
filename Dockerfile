# One image runs the in-cluster controller and the gateway agents on existing
# workers; the controller also copies its own binary to the VDS.
FROM golang:1.26-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags=-s -o /out/k0nnect ./cmd/k0nnect

FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
LABEL org.opencontainers.image.source="https://github.com/kizilov-dev/k0nnect" \
      org.opencontainers.image.description="k0nnect controller and gateway agent"
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends wireguard-tools iproute2 iptables ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/k0nnect /usr/local/bin/k0nnect
ENTRYPOINT ["/usr/local/bin/k0nnect"]
