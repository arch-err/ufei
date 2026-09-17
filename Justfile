default:
    @just --list

test:
    test -z "$(gofmt -l cmd internal)"
    go test -race ./...
    go vet ./...
    helm lint --strict charts/ufei -f examples/values.yaml
    helm template ufei charts/ufei -f examples/values.yaml >/dev/null

build:
    CGO_ENABLED=0 go build -trimpath -o ufei ./cmd/ufei

image tag="ufei:dev":
    docker build -t {{tag}} .
