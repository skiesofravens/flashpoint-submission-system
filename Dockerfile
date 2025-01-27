FROM golang:1.20

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . /app

ARG GIT_COMMIT="git rev-list -1 HEAD"
ENV GIT_COMMIT=$GIT_COMMIT

RUN CGO_ENABLED=0 GOOS=linux go build -o /fpfss ./main/*.go

CMD ["/fpfss"]
