package config

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type envPool struct {
	Max int `json:"max"`
}

type envDB struct {
	Pool *envPool `json:"pool"`
	Name string
}

type envEmbedded struct {
	Region string `json:"region"`
}

type envTyped struct {
	envEmbedded
	Password  string
	Version   string `json:"version"`
	Timeout   time.Duration
	PerMinute int `json:"per_minute"`
	Enabled   bool
	Ratio     float64
	Started   time.Time
	Tags      []string
	DB        envDB  `json:"db"`
	Skipped   string `json:"-"`
}

var _ = Bind[envTyped]("envtyped")

func loadEnvTyped(t *testing.T, vars ...string) (envTyped, error) {
	t.Helper()
	doc, err := EnvSource{Environ: func() []string { return vars }}.Load(context.Background())
	if err != nil {
		return envTyped{}, err
	}
	var got envTyped
	if err := json.Unmarshal(doc["envtyped"], &got); err != nil {
		t.Fatalf("decoding %s: %v", doc["envtyped"], err)
	}
	return got, nil
}

func TestEnvSourceDecodesAgainstBoundType(t *testing.T) {
	got, err := loadEnvTyped(t,
		"ENVTYPED_PASSWORD=123456",
		"ENVTYPED_VERSION=1.10",
		"ENVTYPED_TIMEOUT=5s",
		"ENVTYPED_PER_MINUTE=007",
		"ENVTYPED_ENABLED=true",
		"ENVTYPED_RATIO=+0.5",
		"ENVTYPED_STARTED=2026-01-02T03:04:05Z",
		`ENVTYPED_TAGS=["a","b"]`,
		"ENVTYPED_DB__POOL__MAX=10",
		"ENVTYPED_DB__NAME=main",
		"ENVTYPED_REGION=eu",
		"ENVTYPED_SKIPPED=x",
		"ENVTYPED_UNKNOWN=x",
		"ENVTYPED_DB__UNKNOWN=x",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "123456" || got.Version != "1.10" {
		t.Errorf("number-looking strings must stay strings: %q %q", got.Password, got.Version)
	}
	if got.Timeout != 5*time.Second {
		t.Errorf("duration: got %v", got.Timeout)
	}
	if got.PerMinute != 7 || !got.Enabled || got.Ratio != 0.5 {
		t.Errorf("numbers/bools: %+v", got)
	}
	if !got.Started.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("TextUnmarshaler field: got %v", got.Started)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "b" {
		t.Errorf("JSON slice: got %v", got.Tags)
	}
	if got.DB.Pool == nil || got.DB.Pool.Max != 10 || got.DB.Name != "main" {
		t.Errorf("nested fields: %+v", got.DB)
	}
	if got.Region != "eu" {
		t.Errorf("embedded field: got %q", got.Region)
	}
	if got.Skipped != "" {
		t.Errorf(`json:"-" field must not be set`)
	}
}

func TestEnvSourceInvalidValueNamesVariable(t *testing.T) {
	cases := []string{
		"ENVTYPED_PER_MINUTE=abc",
		"ENVTYPED_PER_MINUTE=1.5",
		"ENVTYPED_TIMEOUT=5", // no unit: refuse rather than mean 5ns
		"ENVTYPED_ENABLED=yes",
		"ENVTYPED_RATIO=NaN",
		"ENVTYPED_RATIO=inf",
		"ENVTYPED_STARTED=yesterday",
		"ENVTYPED_TAGS=a,b",
		"ENVTYPED_DB__POOL__MAX=ten",
	}
	for _, kv := range cases {
		name, _, _ := strings.Cut(kv, "=")
		_, err := loadEnvTyped(t, kv)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want error naming %s, got %v", kv, name, err)
		}
	}
}

func TestEnvSourceNestedConflict(t *testing.T) {
	_, err := loadEnvTyped(t, `ENVTYPED_DB={"Name":"x"}`, "ENVTYPED_DB__NAME=y")
	if err == nil || !strings.Contains(err.Error(), "ENVTYPED_DB") {
		t.Fatalf("whole-field and nested variables for one field should conflict, got %v", err)
	}
}
