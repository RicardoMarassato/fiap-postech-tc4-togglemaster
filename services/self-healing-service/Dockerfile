FROM python:3.12-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates

COPY requirements.txt .
RUN pip install --no-cache-dir --upgrade pip setuptools && \
    pip install --no-cache-dir -r requirements.txt

FROM python:3.12-alpine

WORKDIR /app
RUN apk add --no-cache ca-certificates curl && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S -G appgroup appuser

COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

COPY --chown=appuser:appgroup . .
USER appuser

EXPOSE 8080
CMD ["python", "app.py"]
