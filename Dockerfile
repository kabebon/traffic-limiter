# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orchestrator ./cmd/orchestrator
# Create data dir with nonroot permissions
RUN mkdir /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/orchestrator /app/orchestrator
# Copy the pre-chowned data dir
COPY --from=build --chown=65532:65532 /out/data /data
# SQLite DB volume; nonroot UID/GID 65532 matches the distroless image.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/orchestrator"]
