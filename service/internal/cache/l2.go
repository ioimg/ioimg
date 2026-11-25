package cache

import "context"

// L2 is a shared object-storage cache (S3/R2/MinIO/GCS via S3 gateway).
type L2 interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
}
