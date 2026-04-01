package services

import "context"

// FileStorage is the interface for any storage backend (R2, Supabase, etc.)
type FileStorage interface {
	Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (string, error)
	Download(ctx context.Context, bucket, key string) ([]byte, string, error)
	Delete(ctx context.Context, bucket, key string) error
}
