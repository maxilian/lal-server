# Build
FROM golang:1.25-alpine AS builder
WORKDIR /lal
ENV GOPROXY=https://goproxy.io,direct
COPY . .
RUN go build ./app/lalserver/main.go -o lalserver 

# Output
FROM debian:bookworm-slim
WORKDIR /app
EXPOSE 1935 8080 4433 5544 8083 8084 30000-30100/udp

COPY --from=builder /lal/lalserver /app/lalserver
COPY --from=builder /lal/app/conf/lalserver.conf.json /app/conf/lalserver.conf.json
COPY --from=builder /lal/app/conf/cert.pem /app/conf/cert.pem
COPY --from=builder /lal/app/conf/key.pem /app/conf/key.pem

CMD ["sh","-c","./lalserver -c conf/lalserver.conf.json"]
