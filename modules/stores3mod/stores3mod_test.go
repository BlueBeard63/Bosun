package stores3mod_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bluebeard63/bosun/modules/storemod/storemodtest"
	"github.com/bluebeard63/bosun/modules/stores3mod"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Integration tests run only when BOSUN_S3_ENDPOINT is set, against any
// S3-compatible server, e.g. SeaweedFS (as used in CI):
//
//	docker run -p 8333:8333 -e AWS_ACCESS_KEY_ID=key -e AWS_SECRET_ACCESS_KEY=secret123 \
//	  chrislusf/seaweedfs:4.48 server -s3 -dir=/data -ip.bind=0.0.0.0 -master.volumeSizeLimitMB=1024
//	BOSUN_S3_ENDPOINT=localhost:8333 BOSUN_S3_KEY=key BOSUN_S3_SECRET=secret123 BOSUN_S3_BUCKET=test go test ./...
func TestConformance(t *testing.T) {
	storemodtest.Run(t, newTestStore(t))
}

// TestPutSizes covers the upload paths minio-go chooses by size: a single
// PUT, multipart with a known length, multipart from a stream of unknown
// length, and a file.
func TestPutSizes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	big := make([]byte, 40<<20) // 40 MiB: three 16 MiB parts
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(file, big, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cases := []struct {
		name string
		r    io.Reader
		want []byte
	}{
		{"small known size", bytes.NewReader([]byte("hello")), []byte("hello")},
		{"multipart known size", bytes.NewReader(big), big},
		{"multipart unknown size", io.MultiReader(bytes.NewReader(big)), big},
		{"file", f, big},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := fmt.Sprintf("sizes/%d", i)
			if err := s.Put(ctx, key, c.r); err != nil {
				t.Fatalf("put: %v", err)
			}
			rc, _, err := s.Get(ctx, key)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, c.want) {
				t.Fatalf("round trip: got %d bytes, want %d", len(got), len(c.want))
			}
			_ = s.Delete(ctx, key)
		})
	}
}

// newTestStore connects to the server named by BOSUN_S3_* and ensures the
// bucket exists, or skips the test when BOSUN_S3_ENDPOINT is unset.
func newTestStore(t *testing.T) *stores3mod.S3Store {
	t.Helper()
	ep := os.Getenv("BOSUN_S3_ENDPOINT")
	if ep == "" {
		t.Skip("set BOSUN_S3_ENDPOINT to run the S3 integration tests")
	}
	opts := &stores3mod.Options{
		Endpoint:  ep,
		AccessKey: os.Getenv("BOSUN_S3_KEY"),
		SecretKey: os.Getenv("BOSUN_S3_SECRET"),
		Bucket:    os.Getenv("BOSUN_S3_BUCKET"),
		UseSSL:    false,
	}

	ctx := context.Background()
	cl, err := minio.New(opts.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if ok, _ := cl.BucketExists(ctx, opts.Bucket); !ok {
		if err := cl.MakeBucket(ctx, opts.Bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("make bucket: %v", err)
		}
	}

	s := &stores3mod.S3Store{Opts: opts}
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s
}
