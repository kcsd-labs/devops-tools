# DevOps Tools — single container: the Go binary serves both the API and the UI.

# --- 1. build the frontend -------------------------------------------------
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- 2. build the binary with the UI embedded ------------------------------
FROM golang:1.26 AS build
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
FROM gcr.io/distroless/static-debian12:nonroot
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
