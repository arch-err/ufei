FROM golang:1.26-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ufei ./cmd/ufei

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
LABEL org.opencontainers.image.title="ufei" \
      org.opencontainers.image.description="Bounded egress probes and optional OVN EgressIP recovery" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates iputils-ping \
    && adduser -D -u 65532 ufei
COPY --from=build /ufei /usr/local/bin/ufei
COPY LICENSE /licenses/LICENSE
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ufei"]
