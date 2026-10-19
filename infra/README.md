# Local infrastructure

`compose.yaml` provides the B01 local stack:

| Service | Port | Purpose |
|---------|------|---------|
| PostgreSQL 17 | 5432 | Transactional system of record |
| Redpanda | 9092, 9644 | Kafka-compatible event backbone |
| Redis | 6379 | Non-authoritative cache |
| ClickHouse | 8123, 9009 | Analytics |
| MinIO | 9000, 9001 | S3-compatible object storage |
| OTel Collector | 4317, 4318, 8889 | Trace/metric ingestion |
| Prometheus | 9090 | Local metrics store |

The sample services are behind the `app` Compose profile:

```bash
docker compose --profile app up --build
```

The MinIO service uses a community-maintained image because official community
images are no longer published on Docker Hub. Treat it as local-development
only and pin a verified official image or digest before shared use.
