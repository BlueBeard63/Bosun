// Package stores3mod is an S3-compatible driver for storemod.Store, built on
// the MinIO client so it works with AWS S3, MinIO, Cloudflare R2, and any other
// S3-compatible service. The heavy client library lives only in this module.
//
//	var _ = stores3mod.For()
//
// Configure it with an *Options instance in main:
//
//	registry.RegisterInstance[*stores3mod.Options](app.Reg, &stores3mod.Options{
//	    Endpoint: "s3.amazonaws.com", Bucket: "app-docs",
//	    AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"), SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
//	    UseSSL: true,
//	})
package stores3mod

import (
	"context"
	"io"
	"net/url"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/storemod"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Options configures the S3 connection.
type Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// S3Store stores objects in an S3-compatible bucket.
type S3Store struct {
	Opts   *Options // injected
	client *minio.Client
}

var _ storemod.Store = (*S3Store)(nil)

// Init connects the client.
func (s *S3Store) Init() error {
	c, err := minio.New(s.Opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s.Opts.AccessKey, s.Opts.SecretKey, ""),
		Secure: s.Opts.UseSSL,
		Region: s.Opts.Region,
	})
	if err != nil {
		return err
	}
	s.client = c
	return nil
}

// Put uploads the object, streaming the reader.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, opts ...storemod.PutOption) error {
	cfg := storemod.ResolvePut(opts)
	_, err := s.client.PutObject(ctx, s.Opts.Bucket, key, r, -1, minio.PutObjectOptions{
		ContentType:  cfg.ContentType,
		UserMetadata: cfg.Meta,
	})
	return err
}

// Get downloads the object and its metadata.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, *storemod.Object, error) {
	obj, err := s.client.GetObject(ctx, s.Opts.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, nil, err
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, nil, storemod.ErrNotFound
		}
		return nil, nil, err
	}
	return obj, &storemod.Object{Key: key, Size: info.Size, ModTime: info.LastModified, ContentType: info.ContentType}, nil
}

// Delete removes the object; a missing key is not an error in S3.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.Opts.Bucket, key, minio.RemoveObjectOptions{})
}

// List returns objects whose key begins with prefix.
func (s *S3Store) List(ctx context.Context, prefix string) ([]storemod.Object, error) {
	var out []storemod.Object
	for info := range s.client.ListObjects(ctx, s.Opts.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if info.Err != nil {
			return nil, info.Err
		}
		out = append(out, storemod.Object{Key: info.Key, Size: info.Size, ModTime: info.LastModified, ContentType: info.ContentType})
	}
	return out, nil
}

// Presign returns a presigned URL for the key using S3's native presigning.
func (s *S3Store) Presign(ctx context.Context, key string, op storemod.Op, ttl time.Duration) (string, error) {
	switch op {
	case storemod.OpGet:
		u, err := s.client.PresignedGetObject(ctx, s.Opts.Bucket, key, ttl, url.Values{})
		if err != nil {
			return "", err
		}
		return u.String(), nil
	case storemod.OpPut:
		u, err := s.client.PresignedPutObject(ctx, s.Opts.Bucket, key, ttl)
		if err != nil {
			return "", err
		}
		return u.String(), nil
	default:
		return "", storemod.ErrUnsupported
	}
}

// For registers the S3 store and binds storemod.Store to it.
func For() struct{} {
	bosun.Service[S3Store]()
	bosun.DefaultBind[storemod.Store, S3Store]()
	return struct{}{}
}
