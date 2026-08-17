# Event RPC (request/reply)

Alongside publish/subscribe, the event system supports request/reply, where a caller sends one message and waits for exactly one reply. This is the RPC pattern: a service exposes a subject, and callers invoke it over the same transport that carries events, without an HTTP endpoint. It is supported on the in-memory, NATS, and RabbitMQ backends; Redis Streams does not provide it.

<figure class="diagram">
<svg viewBox="0 0 600 190" role="img" aria-labelledby="rpc-title rpc-desc" xmlns="http://www.w3.org/2000/svg">
<title id="rpc-title">Request and reply</title>
<desc id="rpc-desc">Call sends a request on a subject and blocks for a single reply; OnRequest handles the request and returns the reply.</desc>
<defs>
<marker id="rpc-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="222" y1="88" x2="378" y2="88" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rpc-arw)"/>
<line x1="378" y1="112" x2="222" y2="112" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#rpc-arw)"/>
<text x="300" y="80" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">REQUEST</text>
<text x="300" y="128" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">REPLY</text>
<rect x="40" y="64" width="180" height="60" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="130" y="90" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Requester</text>
<text x="130" y="108" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">Call[Req, Resp]</text>
<rect x="380" y="64" width="180" height="60" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="470" y="90" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Responder</text>
<text x="470" y="108" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">OnRequest</text>
</svg>
<figcaption>Call blocks for one reply; pass a ctx deadline to bound the wait. A remote handler error is returned to the caller as an error.</figcaption>
</figure>

## Handling requests

`OnRequest` registers a typed handler: it decodes each request into `Req`, calls your function, and encodes the returned `Resp` as the reply. Start it in `Init()` like any subscription.

```go
type Pricer struct {
    Bus eventmod.Responder // injected
    sub eventmod.Subscription
}

func (p *Pricer) Init() error {
    s, err := eventmod.OnRequest[Quote, Price](context.Background(), p.Bus, "pricing.quote",
        func(ctx context.Context, q Quote) (Price, error) {
            return p.price(ctx, q)
        })
    p.sub = s
    return err
}

func (p *Pricer) Close() error { return p.sub.Unsubscribe() }
```

## Making requests

`Call` marshals the request, sends it, and unmarshals the single reply into `Resp`. Because it blocks until a reply arrives, always pass a context with a deadline so a missing responder or a slow handler cannot hang the caller.

```go
type Checkout struct {
    Bus eventmod.Requester // injected
}

func (c *Checkout) quote(ctx context.Context, q Quote) (Price, error) {
    ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
    defer cancel()
    return eventmod.Call[Quote, Price](ctx, c.Bus, "pricing.quote", q)
}
```

## Errors, timeouts, and load balancing

When the handler returns an error, that error is propagated back and returned from `Call`, so the caller sees a real failure rather than a malformed reply. When no responder is registered for the subject, `Call` returns `eventmod.ErrNoResponder`. When the deadline passes before a reply arrives, `Call` returns the context error. Registering several responders on one subject with the same `WithGroup` load-balances requests across them, which is how you run a pool of RPC workers.

## Backend support

The in-memory bus answers requests synchronously in the caller's goroutine, which is ideal for tests and single-process apps. NATS uses its native request/reply. RabbitMQ uses the standard reply-to queue and correlation-id pattern. Redis Streams has no request/reply primitive, so the Redis driver does not bind `Requester` or `Responder`; an app that needs RPC should use one of the other backends.
