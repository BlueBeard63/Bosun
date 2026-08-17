# Microservices

Bosun scales down to one process and up to many. In a microservice architecture each service is its own Bosun app in its own module, and they share a single contracts package for the types that cross service boundaries. Services communicate two ways: asynchronously over the event bus, and synchronously through generated typed clients. The `bosun new` command scaffolds this layout so you start from a working workspace.

<figure class="diagram">
<svg viewBox="0 0 720 250" role="img" aria-labelledby="ms-title ms-desc" xmlns="http://www.w3.org/2000/svg">
<title id="ms-title">Microservice topology</title>
<desc id="ms-desc">Two services communicate asynchronously through the event bus and synchronously through a generated typed client, and both import a shared contracts package.</desc>
<defs>
<marker id="ms-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="210" y1="108" x2="278" y2="108" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ms-arw)"/>
<line x1="440" y1="108" x2="508" y2="108" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ms-arw)"/>
<path d="M125 80 V56 a8 8 0 0 1 8 -8 H587 a8 8 0 0 1 8 8 V78" fill="none" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ms-arw)"/>
<text x="360" y="40" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">TYPED CLIENT</text>
<text x="244" y="100" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">PUBLISH</text>
<text x="474" y="100" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">DELIVER</text>
<line x1="125" y1="136" x2="125" y2="178" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<line x1="360" y1="136" x2="360" y2="178" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<line x1="595" y1="136" x2="595" y2="178" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<rect x="40" y="80" width="170" height="56" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="125" y="104" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Service A</text>
<text x="125" y="121" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">bosun app</text>
<rect x="280" y="80" width="160" height="56" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="360" y="104" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Event bus</text>
<text x="360" y="121" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">async</text>
<rect x="510" y="80" width="170" height="56" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="595" y="104" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Service B</text>
<text x="595" y="121" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">bosun app</text>
<rect x="40" y="178" width="640" height="46" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<text x="360" y="205" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">contracts: shared types (events, DTOs, generated clients)</text>
</svg>
<figcaption>Services talk asynchronously through the bus and synchronously through typed clients, and both import the same contracts.</figcaption>
</figure>

## The layout

A workspace ties the modules together with `go.work`.

```
repo/
  go.work
  contracts/            shared types only
  services/
    billing/            one deployable bosun app (own go.mod)
    orders/
```

The one rule that keeps services independent is that `contracts` holds types only, never a `bosun.Service`, `bosun.Controller`, or `bosun.Default` declaration. Because those declarations register through package-level side effects, a controller placed in a shared package would be pulled into every service that imports it. Keeping contracts declaration-free means importing a shared event payload or DTO never drags another service's routes into your process. Each service binary imports only its own controllers plus the driver modules it needs, so its registry is exactly what it declared.

## Communication

Use the [event bus](./events.md) for asynchronous, decoupled communication: one service publishes a `Topic[T]` and another subscribes, without either knowing the other's address. Use a [generated typed client](./client-gen.md) for synchronous request/response: one service calls another's routes with compile-checked method signatures. Both the event payloads and the client's request and response types live in `contracts`, so a change to a shared shape is a compile error in every service that uses it. Each service advertises itself through its [deploy manifest](./manifest.md), which the platform and the client generator consume.

## Scaffolding

`bosun new project` creates the workspace, the contracts package, and a first service; `bosun new service` adds another service to an existing workspace. Both prompt for the module path, the first service name, and the event backend, or take those as flags with `--yes` for non-interactive use.

```bash
bosun new project acme --yes
bosun new service orders --module acme/services/orders --event nats --yes
```

Each generated service is a runnable Bosun app with a sample route, health and manifest endpoints, and the event backend wired in, ready for you to add controllers and services.
