# DevOps Tools — single container: the Go binary serves both the API and the UI.

# --- 1. build the frontend -------------------------------------------------
# Node 24 is the LTS line; 20 left support in April 2026.
FROM node:24-alpine AS web
WORKDIR /web
# The lock file, and npm ci rather than npm install: without it the interface
# was built from whatever the version ranges resolved to on the day, so what
# shipped was not what npm audit had looked at. The lock records the native
# bindings of every platform, so the same file serves this image, CI and a
# developer's machine.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- 2. build the binary with the UI embedded ------------------------------
# The patch release is pinned, and is the same one go.mod names. A floating
# golang:1.26 meant "whichever was newest on release day": 0.25.6 shipped with
# 1.26.8 because 1.26.9 came out a week later, and nothing said so. Pinned, the
# weekly scan reads the same standard library this image contains, and turns
# red when a newer patch fixes something in it — which is the cue to bump both.
FROM golang:1.26.9 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the placeholder page with the real build output before compiling.
RUN rm -rf internal/ui/dist
COPY --from=web /web/dist ./internal/ui/dist
# Stamped into the binary so a running instance can say which build it is.
# Defaults to "dev": an image built without it should not claim a release.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X devops-tools/internal/version.Version=${VERSION}" \
    -o /out/devops-tools ./cmd/server

# --- 3. runtime ------------------------------------------------------------
# Distroless: no shell, no package manager, runs as a non-root user.
#
# Debian 13 rather than 12: bookworm left regular security support in June
# 2026 and gets its fixes only through LTS, which reaches the distroless image
# late — 0.25.6 went out with a tzdata the LTS team had already superseded.
# The binary is static, so the base contributes only certificates, time zones
# and the nonroot user, and 65532 is the same user in both.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/devops-tools /devops-tools
# The distroless nonroot image starts in /home/nonroot, so the relative default
# would not find a config mounted at /config. Be explicit instead.
ENV DEVOPS_TOOLS_CONFIG_DIR=/config
EXPOSE 8080
# Numeric, not the `nonroot` name the base image also answers to: a pod with
# runAsNonRoot cannot start on a name, because the kubelet has no way to resolve
# it to a UID and so cannot confirm it is not root. 65532 is that same user.
USER 65532:65532
ENTRYPOINT ["/devops-tools"]
