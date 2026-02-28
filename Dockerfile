FROM golang:1.25 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /out/zork ./cmd/zork \
 && go build -o /out/web  ./cmd/web  \
 && go build -o /out/mcp  ./cmd/mcp


FROM debian:bookworm-slim

WORKDIR /app
COPY --from=builder /out/zork ./
COPY --from=builder /out/web  ./
COPY --from=builder /out/mcp  ./
COPY zork1.dat ./

EXPOSE 8080 8081

ENTRYPOINT ["./zork"]
CMD ["zork1.dat"]
