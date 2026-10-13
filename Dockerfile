# easyvcs-server: the authoritative EasyVCS server (smart protocol + hosting REST
# + Revision Requests, all under /api/v1).
#
# Built WITH the cluster buildkitd: run ./build-image.sh (targets the shared
# buildkitd, then pushes to the deployment registry via skopeo). No local build
# daemon required.
#
# Build context: the easyvcs repo root (a SINGLE Go module — no go.work, so the
# build does NOT need `cd cmd`). Third-party deps resolve from GOPROXY through
# the build proxy.
#
# The server persists to a single SQL database. The default is SQLite (pure Go,
# via glebarez/sqlite — CGO_ENABLED=0), stored under EASYVCS_HOME (/data), so
# mount a PVC/hostPath there. Postgres/MySQL are also supported via
# EASYVCS_DB_DRIVER / EASYVCS_DB_DSN.
ARG REGISTRY=artifact.worker.svc.cluster.local/root
ARG ALPINE=3.24

# ---- build ----
FROM ${REGISTRY}/golang:1.26-alpine AS build
ARG HTTP_PROXY=http://mihomo.develop.svc.cluster.local:7890
ARG HTTPS_PROXY=http://mihomo.develop.svc.cluster.local:7890
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20,.develop.10.199.64.20.nip.io \
    GOPROXY=https://proxy.golang.org \
    GOWORK=off \
    CGO_ENABLED=0
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY . .
# Single module: build ./cmd/server directly (no `cd cmd`). `-mod=mod` lets Go
# reconcile go.sum if needed; `-trimpath -s -w` keeps the binary small.
RUN go build -mod=mod -trimpath -ldflags="-s -w" -o /out/easyvcs-server ./cmd/server

# ---- runtime ----
FROM ${REGISTRY}/alpine:${ALPINE}
# git is required at runtime: the push-mirror scheduler and gitbridge shell out
# to the git CLI. ca-certificates is needed for outbound TLS (imports/mirrors).
RUN apk add --no-cache ca-certificates git
# /data holds the SQLite DB (and any future on-disk state); mount a PVC there.
COPY --from=build /out/easyvcs-server /usr/local/bin/easyvcs-server
ENV EASYVCS_HOME=/data
VOLUME /data
EXPOSE 8996
ENTRYPOINT ["/usr/local/bin/easyvcs-server"]
CMD ["-addr", ":8996"]
