# Secrets with Infisical

Infisical is a secrets manager, and Bosun reads from it through the same config mechanism as files and environment variables. The `infisicalmod.Source` is a `config.Source` that fetches a project's secrets and exposes each as a config bind key, so a service injects a secret exactly as it would any other configuration value, with no Infisical-specific code in the service.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="sec-title sec-desc" xmlns="http://www.w3.org/2000/svg">
<title id="sec-title">Infisical as a config source</title>
<desc id="sec-desc">The Infisical source loads secrets, which are bound to typed config values that services read through a Dynamic holder.</desc>
<defs>
<marker id="sec-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#sec-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#sec-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#sec-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Infisical</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">secrets API</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">config.Source</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">Load()</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Dynamic[T]</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">bound key</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Service</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">.Get()</text>
</svg>
<figcaption>The Infisical source plugs into the same config pipeline as files and environment variables, so services stay unaware of where a secret came from.</figcaption>
</figure>

## Registering the source

Add the source in `main` with your project id, environment, and a machine-identity or service token. Register it alongside other sources; later sources override earlier ones, so an Infisical secret can override a file default.

```go
func main() {
    app := bosun.New()
    config.Add(config.FileSource{Path: "config.json"}) // defaults
    config.Add(infisicalmod.Source{
        ProjectID:   os.Getenv("INFISICAL_PROJECT_ID"),
        Environment: "prod",
        Token:       os.Getenv("INFISICAL_TOKEN"),
    })
    log.Fatal(app.Run(":8080"))
}
```

## Binding a secret

Bind a config key to a typed value, then inject the value where you need it. An opaque secret binds to a string; a secret whose value is a JSON object binds to a struct.

```go
var _ = config.Bind[string]("DB_PASSWORD")

type Database struct {
    Password *bosun.Dynamic[string] // injected
}

func (d *Database) dsn() string {
    return "postgres://app:" + *d.Password.Get() + "@db/app"
}
```

Because the watcher polls its sources, rotating a secret in Infisical updates the bound value on the next poll, so a rotation takes effect without a redeploy. The [config guide](./config.md) covers binding, layering, and the watcher in full.

## Values and encoding

Infisical secrets are key/value pairs. A value that is a JSON object or array is exposed as raw JSON so it can bind to a struct; every other value is exposed as a JSON string, which is the right shape for the common case of an opaque secret. Store a structured secret as a single JSON value in Infisical when you want it to bind to a struct.
