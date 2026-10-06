package stores3mod_test

import (
	"context"
	"os"
	"testing"

	"github.com/bluebeard63/bosun/modules/storemod/storemodtest"
	"github.com/bluebeard63/bosun/modules/stores3mod"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Integration test runs only when BOSUN_S3_ENDPOINT is set, against any
// S3-compatible server, e.g. SeaweedFS (as used in CI):
//
//	docker run -p 8333:8333 -e AWS_ACCESS_KEY_ID=key -e AWS_SECRET_ACCESS_KEY=secret123 \
//	  chrislusf/seaweedfs:4.48 server -s3 -dir=/data -ip.bind=0.0.0.0 -master.volumeSizeLimitMB=1024
//	BOSUN_S3_ENDPOINT=localhost:8333 BOSUN_S3_KEY=key BOSUN_S3_SECRET=secret123 BOSUN_S3_BUCKET=test go test ./...
func TestConformance(t *testing.T) {
	ep := os.Getenv("BOSUN_S3_ENDPOINT")
	if ep == "" {
		t.Skip("set BOSUN_S3_ENDPOINT to run the S3 integration test")
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
	storemodtest.Run(t, s)
}
