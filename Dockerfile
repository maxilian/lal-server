# Build
FROM golang:1.26-bookworm AS builder
WORKDIR /lal
ENV CGO_ENABLED=1
#ENV GOPROXY=https://goproxy.io,direct
COPY . .
RUN apt update && apt install -y --no-install-recommends build-essential
RUN go mod download
RUN go build -o lalserver ./app/lalserver/main.go 

# Output
FROM debian:bookworm-slim
RUN apt update && apt install -y --no-install-recommends ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
EXPOSE 1935 8080 4433 5544 8083 8084 30000-30100/udp

COPY --from=builder /lal/lalserver /app/lalserver
COPY --from=builder /lal/conf/lalserver.conf.json /app/conf/lalserver.conf.json
COPY --from=builder /lal/conf/cert.pem /app/conf/cert.pem
COPY --from=builder /lal/conf/key.pem /app/conf/key.pem

CMD ["sh","-c","./lalserver -c conf/lalserver.conf.json"]
