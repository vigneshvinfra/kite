go mod init myapp 
docker build -t myapp:v1 .
docker run -p 8000:8000 myapp:v1

multistage docker build
security:
nonroot
distroless