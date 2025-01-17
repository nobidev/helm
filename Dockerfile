FROM golang:1.18
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY ./ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o helm ./cmd/helm
