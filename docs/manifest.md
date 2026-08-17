# Deploy manifest

A Bosun service already knows its own shape: its routes, its health endpoints, and the event subjects it produces and consumes. The deploy manifest exposes that as JSON so the deployment platform can wire the service up without hand-maintained configuration, and so a Caddyfile can be generated from it. The manifest is assembled from the framework's own introspection, not from a file you keep in sync.

<figure class="diagram">
<svg viewBox="0 0 720 210" role="img" aria-labelledby="mf-title mf-desc" xmlns="http://www.w3.org/2000/svg">
<title id="mf-title">The deploy manifest</title>
<desc id="mf-desc">A Bosun app's routes, health, and queues are assembled into a manifest that the deploy dashboard and a Caddyfile generator consume.</desc>
<defs>
<marker id="mf-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="180" y1="110" x2="258" y2="110" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mf-arw)"/>
<line x1="432" y1="82" x2="518" y2="82" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mf-arw)"/>
<line x1="432" y1="140" x2="518" y2="140" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#mf-arw)"/>
<text x="219" y="102" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">ASSEMBLE</text>
<rect x="20" y="80" width="160" height="60" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="100" y="106" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Bosun app</text>
<text x="100" y="124" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">routes, health, queues</text>
<rect x="260" y="50" width="170" height="120" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="345" y="106" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="14" font-weight="600" fill="var(--accent)">Manifest</text>
<text x="345" y="126" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">/.bosun/manifest</text>
<rect x="520" y="56" width="180" height="52" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="610" y="78" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Deploy dashboard</text>
<text x="610" y="95" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">wires the service</text>
<rect x="520" y="114" width="180" height="52" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="610" y="136" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Caddyfile</text>
<text x="610" y="153" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">reverse proxy</text>
</svg>
<figcaption>Routes come from the typed route index, queues from event declarations, and the rest from registered contributors.</figcaption>
</figure>

## The runtime endpoint

Importing the manifest module mounts `GET /.bosun/manifest`, which serves the live manifest. Configure the service name, version, and port with an `*Options` instance.

```go
registry.RegisterInstance[*manifestmod.Options](app.Reg, &manifestmod.Options{
    Service: "billing", Version: buildVersion, Port: 8080,
})
```

The response lists each route with its method, path, operation, and declared statuses; the health endpoints; and every event subject the service declared, with its direction.

```json
{
  "service": "billing",
  "version": "v1.2.3",
  "port": 8080,
  "routes": [{ "method": "POST", "path": "/api/users", "operation": "..." }],
  "health": { "live": "/health/live", "ready": "/health/ready" },
  "queues": [{ "subject": "users.created", "direction": "produces" }]
}
```

## Declaring queues and contributors

Routes and health are inferred automatically. To advertise the event subjects a service uses, declare them next to your topics with `eventmod.Declare`; they then appear under `queues`. To advertise anything the framework cannot infer, such as required environment variables or secrets, register a `Contributor`.

```go
var _ = eventmod.Declare("users.created", eventmod.Produces, "")

var _ = manifestmod.Register(manifestmod.ContributorFunc(func(m *manifestmod.Manifest) {
    m.Secrets = append(m.Secrets, manifestmod.SecretRef{Name: "DB_PASSWORD"})
}))
```

## From the command line

The `bosun manifest` command fetches a running service's manifest, and with `--caddy` prints a Caddy reverse-proxy site block derived from it. For a build-time step that does not run a server, call `manifestmod.EmitIfRequested` in `main` after `app.Start()`; with `BOSUN_MANIFEST=1` set, the process prints the manifest and exits.

```bash
bosun manifest http://localhost:8080
bosun manifest --caddy http://localhost:8080
```
