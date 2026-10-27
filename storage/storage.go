// Package storage stores binary artifacts (screenshots, offloaded page bodies)
// on the local disk or in an S3-compatible object store.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/resolver/crawler/config"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// Store is a minimal object store.
type Store interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	// DeletePrefix removes every object whose key starts with prefix.
	DeletePrefix(ctx context.Context, prefix string) error
	Driver() string
}

// New builds the store selected in the configuration.
func New(cfg config.StorageConfig) (Store, error) {
	switch cfg.Driver {
	case "", "local":
		dir := cfg.LocalDir
		if dir == "" {
			dir = "data/objects"
		}
		return NewLocal(dir)
	case "s3":
		return NewS3(cfg.S3)
	}
	return nil, fmt.Errorf("unknown storage driver %q", cfg.Driver)
}

// validKey rejects keys that could escape the store root.
func validKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.Contains(key, "\\") {
		return fmt.Errorf("storage: invalid key %q", key)
	}
	return nil
}

// JobPrefix is the key prefix under which all artifacts of a job are stored.
func JobPrefix(jobID string) string { return "jobs/" + jobID + "/" }

// ScreenshotKey is the key of a page screenshot.
func ScreenshotKey(jobID string, pageID uint) string {
	return fmt.Sprintf("%sscreenshots/%d.png", JobPrefix(jobID), pageID)
}

// BodyKey is the key of an offloaded page body.
func BodyKey(jobID, urlHash string) string {
	return JobPrefix(jobID) + "bodies/" + urlHash
}

// Local stores objects as files under a root directory.
type Local struct{ root string }

// NewLocal creates the root directory if needed.
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Local{root: root}, nil
}

// Driver returns "local".
func (l *Local) Driver() string { return "local" }

// Put writes an object atomically.
func (l *Local) Put(_ context.Context, key string, data []byte, _ string) error {
	if err := validKey(key); err != nil {
		return err
	}
	path := filepath.Join(l.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get reads an object.
func (l *Local) Get(_ context.Context, key string) ([]byte, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// DeletePrefix removes a key prefix; prefixes must end with "/".
func (l *Local) DeletePrefix(_ context.Context, prefix string) error {
	if err := validKey(prefix); err != nil || !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("storage: invalid prefix %q", prefix)
	}
	return os.RemoveAll(filepath.Join(l.root, filepath.FromSlash(strings.TrimSuffix(prefix, "/"))))
}

// S3 stores objects in an S3-compatible bucket.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3 connects to the bucket (it must already exist).
func NewS3(cfg config.S3Config) (*S3, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: client, bucket: cfg.Bucket, prefix: prefix}, nil
}

// Driver returns "s3".
func (s *S3) Driver() string { return "s3" }

// Put uploads an object.
func (s *S3) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if err := validKey(key); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Get downloads an object.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.prefix+key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// DeletePrefix removes every object under prefix.
func (s *S3) DeletePrefix(ctx context.Context, prefix string) error {
	if err := validKey(prefix); err != nil {
		return err
	}
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + prefix, Recursive: true})
	ch := make(chan minio.ObjectInfo)
	go func() {
		defer close(ch)
		for o := range objects {
			if o.Err != nil {
				continue
			}
			ch <- o
		}
	}()
	// Drain every result so the producer goroutines never block.
	var firstErr error
	for e := range s.client.RemoveObjects(ctx, s.bucket, ch, minio.RemoveObjectsOptions{}) {
		if e.Err != nil && firstErr == nil {
			firstErr = e.Err
		}
	}
	return firstErr
}
