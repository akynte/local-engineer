FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/svc ./...

FROM debian:bookworm-slim
ENV DB_URL=""
ENV LISTEN_ADDR=":8080"
COPY --from=build /out/svc /usr/local/bin/svc
