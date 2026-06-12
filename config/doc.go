// Package config hot-reloads configuration into bosun.Dynamic values from
// any number of layered sources: JSON files, environment variables / .env,
// database key-value tables (optionally encrypted at rest), or anything
// implementing Source.
//
// Sources are layered in registration order — later sources override earlier
// ones per key:
//
//	var _ = config.Bind[mw.RateLimitOptions]("ratelimit")
//
//	// in main:
//	config.Add(config.FileSource{Path: "config.json"})        // base
//	config.Add(config.EnvSource{})                            // overrides file
//	config.Add(config.KVSource{Store: table, Decrypt: box.Open}) // DB wins
//
// The watcher polls all sources; only keys whose bytes actually changed are
// re-applied, so Dynamic subscribers aren't spammed.
package config
