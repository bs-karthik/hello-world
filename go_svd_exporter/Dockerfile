#docker build -t systemone/svdpromethues .
FROM golang:1.23.3

WORKDIR /app
COPY . .

#RUN go mod init goldap
RUN go mod tidy
RUN go build -o main .

CMD ["/app/main"]
