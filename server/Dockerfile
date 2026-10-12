# One image for the server, the worker, and the two administrator commands.
# Each ECS service picks its program with the container's command.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/server ./cmd/worker ./cmd/migrate ./cmd/adddirector

# No shell and no package manager; it has the CA certificates the TLS
# connections to AWS, Temporal Cloud, and the providers need. Runs as a
# non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
