FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/email-mcp .

FROM alpine:3.20
RUN adduser -D -u 10001 mailer
USER mailer
COPY --from=build /out/email-mcp /usr/local/bin/email-mcp
EXPOSE 8000
ENTRYPOINT ["email-mcp"]
