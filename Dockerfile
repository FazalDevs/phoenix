# Build the chess server (Phoenix SDK + chess rules) into a tiny static binary.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /phoenix ./cmd/chess

# Distroless static image: ~2MB base, includes CA certs for TLS to managed
# Postgres/Redis (Neon, Upstash, etc.).
FROM gcr.io/distroless/static-debian12
COPY --from=build /phoenix /phoenix
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/phoenix"]
