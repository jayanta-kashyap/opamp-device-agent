# POC Provisioner - Test Device Deployment
# This is a POC-only component. Not for production use.

FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/poc-provisioner .

FROM gcr.io/distroless/base-debian11

COPY --from=builder /out/poc-provisioner /poc-provisioner

EXPOSE 8090

ENTRYPOINT ["/poc-provisioner"]
