package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
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
// their bound Dynamics. It implements io.Closer, so app shutdown stops the
// poll loop.
type Watcher struct {
	reg  *registry.Registry // injected (the app registers itself)
	opts *Options

	sources []Source
	applied map[string]string // key -> last-seen raw bytes (applied or rejected)
	restore map[string]func() // key -> puts the pre-config default back

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

var _ = bosun.Service[Watcher]()

func (w *Watcher) Init() error {
	w.applied = map[string]string{}
	w.restore = map[string]func(){}
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
	// startup load failures are fatal: fail fast
	keyErrs, err := w.load(context.Background())
	if err != nil {
		return err
	}
	if len(keyErrs) > 0 {
		return errors.Join(keyErrs...)
	}
	poll := w.opts.Poll
	if poll <= 0 {
		poll = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			// runtime reload failures keep the previous config; per-key
			// apply errors are logged by load
			if _, err := w.load(ctx); err != nil && ctx.Err() == nil {
				slog.Error("config reload failed", "error", err)
			}
		}
	}()
	return nil
}

// Close stops the poll loop and waits for an in-flight load to return.
func (w *Watcher) Close() error {
	if w.cancel == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.cancel()
		<-w.done
	})
	return nil
}

// load merges every source and applies each changed binding. A source error
// aborts the load (err) so a partial view is never applied; a binding that
// fails to apply is logged and collected in keyErrs without blocking the
// others.
func (w *Watcher) load(ctx context.Context) (keyErrs []error, err error) {
	merged := map[string]json.RawMessage{}
	docs := make([]map[string]json.RawMessage, len(w.sources))
	for i, s := range w.sources {
		doc, err := s.Load(ctx)
		if err != nil {
			return nil, err
		}
		docs[i] = doc
		for k, v := range doc {
			if prev, ok := merged[k]; ok {
				v = mergeJSON(prev, v) // later sources override per field
			}
			merged[k] = v
		}
	}
	for _, b := range bindings {
		raw, ok := merged[b.key]
		if !ok {
			if _, seen := w.applied[b.key]; seen {
				delete(w.applied, b.key)
				if restore := w.restore[b.key]; restore != nil {
					restore()
					slog.Info("config reset to default", "key", b.key)
				}
			}
			continue
		}
		if last, seen := w.applied[b.key]; seen && last == string(raw) {
			continue
		}
		// remember these bytes even on failure so a bad value is reported
		// once, not on every poll
		w.applied[b.key] = string(raw)
		err := w.checkUnknown(b, docs)
		if err == nil {
			err = w.apply(b, raw)
		}
		if err != nil {
			slog.Error("config apply failed", "key", b.key, "error", err)
			keyErrs = append(keyErrs, err)
			continue
		}
		slog.Info("config applied", "key", b.key)
	}
	return keyErrs, nil
}

// checkUnknown rejects b's key when any source's document for it has fields
// the bound type doesn't declare. Each source is checked before merging, so
// the error names the source that holds the typo and the field's full path.
func (w *Watcher) checkUnknown(b binding, docs []map[string]json.RawMessage) error {
	var errs []error
	for i, doc := range docs {
		raw, ok := doc[b.key]
		if !ok {
			continue
		}
		var unknown []string
		unknownFields(b.typ, raw, "", &unknown)
		if len(unknown) > 0 {
			errs = append(errs, fmt.Errorf("config key %q: unknown field %s in %s",
				b.key, quoteJoin(unknown), describeSource(w.sources[i])))
		}
	}
	return errors.Join(errs...)
}

func quoteJoin(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

func (w *Watcher) apply(b binding, raw json.RawMessage) error {
	if _, ok := w.restore[b.key]; !ok {
		restore, err := b.snapshot(w.reg)
		if err != nil {
			return err
		}
		w.restore[b.key] = restore
	}
	return b.apply(w.reg, raw)
}

// mergeJSON deep-merges over into base: when both are objects, fields are
// merged recursively (matched case-insensitively, like encoding/json does
// when decoding); otherwise over replaces base.
func mergeJSON(base, over json.RawMessage) json.RawMessage {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(base, &a) != nil || json.Unmarshal(over, &b) != nil || a == nil || b == nil {
		return over
	}
	for k, v := range b {
		name := k
		if _, ok := a[k]; !ok {
			for ak := range a {
				if strings.EqualFold(ak, k) {
					name = ak
					break
				}
			}
		}
		if prev, ok := a[name]; ok {
			v = mergeJSON(prev, v)
		}
		a[name] = v
	}
	out, err := json.Marshal(a)
	if err != nil {
		return over
	}
	return out
}
