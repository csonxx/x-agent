FROM golang:1.25 AS build

WORKDIR /src/xxx-code

COPY xxx-code/go.mod xxx-code/go.sum ./
RUN go mod download

COPY xxx-code/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/xxx-code ./cmd/xxx-code && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/xxx-code-stability ./cmd/xxx-code-stability

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /workspace

COPY --from=build /out/xxx-code /usr/local/bin/xxx-code
COPY --from=build /out/xxx-code-stability /usr/local/bin/xxx-code-stability

EXPOSE 7331

ENTRYPOINT ["/usr/local/bin/xxx-code"]
CMD ["--daemon", "--config", "/etc/xxx-code/config.yaml"]
