package cache

import (
	"bytes"
	"sync/atomic"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Options configures an S3-compatible L2 cache.
type S3Options struct {
	Endpoint  string // host[:port] or full URL
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
	UseSSL    bool
	SecureSet bool // when false, scheme in Endpoint decides
}

// S3 is an L2 cache backed by any S3-compatible object store.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3 builds an S3 client. The bucket is created if it does not exist.
func NewS3(ctx context.Context, o S3Options) (*S3, error) {
	endpoint := strings.TrimSpace(o.Endpoint)
	secure := o.UseSSL
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("l2 endpoint: %w", err)
		}
		if !o.SecureSet {
			secure = u.Scheme == "https"
		}
		endpoint = u.Host
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" || o.Bucket == "" || o.AccessKey == "" || o.SecretKey == "" {
		return nil, fmt.Errorf("l2 requires endpoint, bucket, access key and secret key")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: secure,
		Region: o.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("l2 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("l2 bucket check: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, o.Bucket, minio.MakeBucketOptions{Region: o.Region}); err != nil {
			return nil, fmt.Errorf("l2 create bucket: %w", err)
		}
	}
	prefix := strings.Trim(o.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: client, bucket: o.Bucket, prefix: prefix}, nil
}

func (s *S3) object(key string) string { return s.prefix + key }

// Get reads an object. A missing key is (nil, false, nil), not an error.
func (s *S3) Get(ctx context.Context, key string) ([]byte, bool, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.object(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, false, err
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// Stat reports whether key exists and its size, using a HEAD request (Class B
// on S3-compatible stores) instead of downloading it.
func (s *S3) Stat(ctx context.Context, key string) (int64, bool, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.object(key), minio.StatObjectOptions{})
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
			return 0, false, nil
		}
		return 0, false, err
	}
	return info.Size, true, nil
}

// Put writes an object.
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.object(key),
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

// Delete removes one object.
func (s *S3) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, s.object(key), minio.RemoveObjectOptions{})
}

// DeletePrefix removes every object under prefix (relative to the L2 prefix).
//
// S3 has no batch PUT, but it does have a batch DELETE (up to 1000 keys per
// request, RemoveObjects), so purging a whole prefix costs one Class A
// operation per 1000 objects instead of one per object.
func (s *S3) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	objects := make(chan minio.ObjectInfo, 1000)
	listErr := make(chan error, 1)
	var listed atomic.Int64
	go func() {
		defer close(objects)
		for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
			Prefix:    s.object(prefix),
			Recursive: true,
		}) {
			if obj.Err != nil {
				listErr <- obj.Err
				return
			}
			listed.Add(1)
			select {
			case objects <- obj:
			case <-ctx.Done():
				listErr <- ctx.Err()
				return
			}
		}
	}()

	var firstErr error
	for rmErr := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
		if rmErr.Err != nil && firstErr == nil {
			firstErr = rmErr.Err
		}
	}
	select {
	case err := <-listErr:
		if firstErr == nil {
			firstErr = err
		}
	default:
	}
	return int(listed.Load()), firstErr
}
