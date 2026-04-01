package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type R2Service struct {
	client    *s3.Client
	publicURL string
}

func NewR2Service(accountID, accessKeyID, secretAccessKey, publicURL string) (*R2Service, error) {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load R2 config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &R2Service{client: client, publicURL: publicURL}, nil
}

func (r *R2Service) Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        io.NopCloser(bytes.NewReader(data)),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("R2 upload failed: %w", err)
	}

	url := fmt.Sprintf("%s/%s/%s", r.publicURL, bucket, key)
	return url, nil
}

func (r *R2Service) Delete(ctx context.Context, bucket, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("R2 delete failed: %w", err)
	}
	return nil
}

func (r *R2Service) Download(ctx context.Context, bucket, key string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("R2 download failed: %w", err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read R2 object: %w", err)
	}

	contentType := ""
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	return data, contentType, nil
}
