FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /panel ./cmd/server && CGO_ENABLED=0 go build -o /agent ./cmd/agent
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /panel /usr/local/bin/panel
COPY --from=build /agent /usr/local/bin/agent
WORKDIR /app
COPY web ./web
COPY --from=build /agent /app/web/downloads/agent
COPY install-agent.sh ./install-agent.sh
ENV DATABASE_PATH=/data/panel.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["panel"]
