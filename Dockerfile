FROM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o bin/external-checkpointer cmd/main.go

FROM alpine:3.19
WORKDIR /
COPY --from=builder /workspace/bin/external-checkpointer /external-checkpointer

# Explicitly set the ownership for non-root user 65532
RUN chmod +x /external-checkpointer && chown 65532:65532 /external-checkpointer

USER 65532:65532
ENTRYPOINT ["/external-checkpointer"]
