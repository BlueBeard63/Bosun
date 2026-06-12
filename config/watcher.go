package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/registry"
)

// Options configures the watcher.
type Options struct {
	Poll time.Duration // poll interval, default 1s
}

var _ = bosun.Default[*Options](func() *Options {
	return &Options{Poll: time.Second}
})

// --- watcher ---

// Watcher merges all sources on a poll loop and applies changed keys to
// their bound Dynamics.
type Watcher struct {
	reg  *registry.Registry // injected (the app registers itself)
	opts *Options

	sources []Source
	applied map[string]string // key -> last-applied raw bytes
}

var _ = bosun.Service[Watcher]()

func (w *Watcher) Init() error {
	w.applied = map[string]string{}
	for _, build := range pendingSources {
		s, err := build(w.reg)
		if err != nil {
			return err
		}
		w.sources = append(w.sources, s)
	}
	if len(w.sources) == 0 {
		return nil
	}
	if err := w.load(context.Background()); err != nil {
		return err // startup load failures are fatal: fail fast
	}
	poll := w.opts.Poll
	if poll <= 0 {
		poll = time.Second
	}
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for range t.C {
			if err := w.load(context.Background()); err != nil {
				// runtime reload failures keep the previous config
				slog.Error("config reload failed", "error", err)
			}
		}
	}()
	return nil
}

func (w *Watcher) load(ctx context.Context) error {
	merged := map[string]json.RawMessage{}
	for _, s := range w.sources {
		doc, err := s.Load(ctx)
		if err != nil {
			return err
		}
		for k, v := range doc {
			merged[k] = v // later sources override earlier ones
		}
	}
	for _, b := range bindings {
		raw, ok := merged[b.key]
		if !ok || string(raw) == w.applied[b.key] {
			continue
		}
		if err := b.apply(w.reg, raw); err != nil {
			return err
		}
		w.applied[b.key] = string(raw)
		slog.Info("config applied", "key", b.key)
	}
	return nil
}
