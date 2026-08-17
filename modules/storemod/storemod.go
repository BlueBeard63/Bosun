// Package storemod is the driver-agnostic contract for blob/object storage on
// top of Bosun. Services depend on storemod.Store to read and write objects by
// key; a driver module (storefsmod, stores3mod, storewebdavmod) registers the
// concrete implementation. Hosts swap drivers without touching application code,
// the same way the repo module swaps databases.
//
//	import "github.com/amberstack/bosun/modules/storemod"
//	import "github.com/amberstack/bosun/modules/storefsmod"
//
//	var _ = storefsmod.For()
//
//	type Documents struct {
//	    Store storemod.Store // injected
//	}
package storemod

import (
	"context"
	"errors"
	"io"
	"time"
)

// Object is the metadata for a stored object.
type Object struct {
	Key         string
	Size        int64
	ModTime     time.Time
	ContentType string
}

// Op is the operation a presigned URL authorizes.
type Op int

const (
	// OpGet authorizes a download.
	OpGet Op = iota
	// OpPut authorizes an upload.
	OpPut
)

// Store reads and writes objects by key. Get returns ErrNotFound when the key
// does not exist; Presign returns ErrUnsupported on drivers that cannot sign URLs.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, opts ...PutOption) error
	Get(ctx context.Context, key string) (io.ReadCloser, *Object, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]Object, error)
	Presign(ctx context.Context, key string, op Op, ttl time.Duration) (string, error)
}

var (
	// ErrNotFound is returned by Get when no object has the key. Drivers
	// translate their own not-found sentinels into this value.
	ErrNotFound = errors.New("store: object not found")
	// ErrUnsupported is returned for an operation a driver cannot perform,
	// such as presigning on a local filesystem with no signing secret.
	ErrUnsupported = errors.New("store: operation unsupported by driver")
)

// PutConfig is the resolved set of put options.
type PutConfig struct {
	ContentType string
	Meta        map[string]string
}

// PutOption configures a single Put call.
type PutOption func(*PutConfig)

// WithContentType sets the stored object's content type.
func WithContentType(ct string) PutOption {
	return func(c *PutConfig) { c.ContentType = ct }
}

// WithMeta attaches user metadata to the object (where the driver supports it).
func WithMeta(m map[string]string) PutOption {
	return func(c *PutConfig) {
		if c.Meta == nil {
			c.Meta = map[string]string{}
		}
		for k, v := range m {
			c.Meta[k] = v
		}
	}
}

// ResolvePut applies opts and returns the resolved config. Drivers call this.
func ResolvePut(opts []PutOption) PutConfig {
	var c PutConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}
