# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/portwatch-vi-rss .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/portwatch-vi-rss /usr/local/bin/portwatch-vi-rss
COPY config.yaml /etc/portwatch-vi-rss/config.yaml
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/portwatch-vi-rss", "-config", "/etc/portwatch-vi-rss/config.yaml", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/portwatch-vi-rss"]
CMD ["-config", "/etc/portwatch-vi-rss/config.yaml"]
