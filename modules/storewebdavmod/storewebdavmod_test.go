package storewebdavmod

import (
	"context"
	"os"
	"testing"

	"github.com/amberstack/bosun/modules/storemod/storemodtest"
)

func TestParseMultistatus(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/dav/user/docs/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/a.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>5</d:getcontentlength>
      <d:getcontenttype>text/plain</d:getcontenttype>
      <d:resourcetype/>
    </d:prop></d:propstat>
  </d:response>
  <d:response>
    <d:href>/dav/user/docs/b%20c.txt</d:href>
    <d:propstat><d:prop>
      <d:getcontentlength>1</d:getcontentlength>
      <d:resourcetype/>
    </d:prop></d:propstat>
  </d:response>
</d:multistatus>`)

	objs, err := parseMultistatus(body, "/dav/user", "docs/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2 (collection skipped)", len(objs))
	}
	byKey := map[string]int64{}
	for _, o := range objs {
		byKey[o.Key] = o.Size
	}
	if byKey["docs/a.txt"] != 5 {
		t.Fatalf("a.txt size = %d, want 5", byKey["docs/a.txt"])
	}
	if _, ok := byKey["docs/b c.txt"]; !ok {
		t.Fatalf("expected url-decoded key 'docs/b c.txt', got keys %v", byKey)
	}
}

// Integration test runs only when BOSUN_WEBDAV_URL is set.
func TestConformance(t *testing.T) {
	base := os.Getenv("BOSUN_WEBDAV_URL")
	if base == "" {
		t.Skip("set BOSUN_WEBDAV_URL to run the WebDAV integration test")
	}
	s := &WebDAVStore{Opts: &Options{
		BaseURL:  base,
		Username: os.Getenv("BOSUN_WEBDAV_USER"),
		Password: os.Getenv("BOSUN_WEBDAV_PASS"),
	}}
	// clear any leftovers from a prior run
	_ = s.Delete(context.Background(), "docs/a.txt")
	storemodtest.Run(t, s)
}
