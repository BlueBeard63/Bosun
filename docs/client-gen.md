# Typed service clients

When one service calls another over HTTP, you want a compile-checked client rather than hand-written requests. `bosun gen client` reads a service's deploy manifest and generates a typed Go client with one method per route, where each method's request and response types are the handler's own In and Out types. A change to a route's shape becomes a compile error in every caller.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="cg-title cg-desc" xmlns="http://www.w3.org/2000/svg">
<title id="cg-title">Generating a typed client</title>
<desc id="cg-desc">A service manifest is read by the generator, which emits a typed client that another service uses to call it.</desc>
<defs>
<marker id="cg-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cg-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cg-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#cg-arw)"/>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Manifest</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">routes, In/Out</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">gen client</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">text/template</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Typed client</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">one method/route</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Caller</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">compile-checked</text>
</svg>
<figcaption>The generated method signatures use the handler's request and response types, so a route change breaks its callers at compile time.</figcaption>
</figure>

## Sharing types

For the generated client to compile, the request and response types must be importable by the caller. Put your DTOs in a shared `contracts` package that both the service and its callers import, and use those types in your handlers. The manifest records each route's In and Out type together with its import path, and the generator emits the matching import and method signatures. Types that are unnamed, such as `struct{}` for a bodyless route, need no import.

## Generating

Point the generator at a running service, or at a saved manifest file. It prints the client to stdout, or writes it to a file with `--out`.

```bash
bosun gen client http://billing:8080 --package clients --out clients/billing.go
```

Each route becomes a method whose name comes from the handler, whose path parameters become string arguments, and whose body and result are the handler's types.

```go
client := clients.NewBillingClient("http://billing:8080")
user, err := client.Get(ctx, "42")
created, err := client.Create(ctx, contracts.CreateUserIn{Email: "a@b.dev"})
```

## What the client does

A generated method marshals the request as JSON, substitutes path parameters (URL-escaped), sends the request, and decodes the response into the typed result. A non-2xx response returns an error that includes the status and the response body. The client exposes a `BaseURL` and an `HTTP *http.Client` field, so you can point it at a different address or supply a client with timeouts, tracing, or authentication. Because the code is generated, regenerate it whenever the service's routes change, and commit the result.
