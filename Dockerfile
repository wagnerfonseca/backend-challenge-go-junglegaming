# Build stage pinned to the module Go version.
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/service ./cmd/service
RUN CGO_ENABLED=0 go build -o /out/migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -o /out/provision ./cmd/provision

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/service /usr/local/bin/service
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/provision /usr/local/bin/provision
COPY migrations /migrations
WORKDIR /
ENTRYPOINT ["/usr/local/bin/service"]
