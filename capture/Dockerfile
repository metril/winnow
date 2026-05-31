# winnow capture: Go supervisor + rtl_tcp (rtl-sdr) + rtlamr (Go).
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/capture ./cmd/capture

FROM golang:1.22-bookworm AS rtlamr
RUN go install github.com/bemasher/rtlamr@latest

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends rtl-sdr \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/capture /usr/local/bin/capture
COPY --from=rtlamr /go/bin/rtlamr /usr/local/bin/rtlamr
CMD ["capture"]
