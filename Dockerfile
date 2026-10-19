FROM golang:1.26.4 AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY javy/go.mod javy/
COPY appletpb/go.mod appletpb/go.sum appletpb/
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /appletd ./cmd/appletd

# The image distributes Go, every module in the binary, and the Javy plugin
# and TypeScript compiler that are embedded in it, so it passes on their
# licenses and notices.
RUN set -e; \
    mkdir -p /licenses/go; \
    cp "$(go env GOROOT)/LICENSE" /licenses/go/; \
    go list -deps -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' ./cmd/appletd | sort -u | \
    while read -r mod dir; do \
        mkdir -p "/licenses/$mod"; \
        find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'NOTICE*' -o -iname 'COPYING*' \) \
            -exec cp {} "/licenses/$mod/" \; ; \
    done; \
    tsc="$(go list -m -f '{{.Dir}}' github.com/skybitsnl/go-tsc/v7)"; \
    mkdir -p /licenses/typescript; \
    cp "$tsc/package/LICENSE" "$tsc/package/NOTICE.txt" /licenses/typescript/

# Static, with a writable /tmp for tsc, which appletd extracts at startup.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /appletd /appletd
COPY --from=build /licenses /licenses
EXPOSE 2080
ENTRYPOINT ["/appletd", "-listen=:2080"]
