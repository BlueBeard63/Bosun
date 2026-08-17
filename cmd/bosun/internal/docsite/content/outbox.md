# Transactional outbox

Publishing an event and committing a database change are two separate operations, so a crash between them either loses the event or emits one for a change that rolled back. The transactional outbox closes that gap: you write the event into an outbox table inside the same transaction as the business change, and a background relay publishes the pending rows afterward. The event is therefore published if and only if its transaction committed, at the cost of at-least-once delivery, so consumers must be idempotent.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="ob-title ob-desc" xmlns="http://www.w3.org/2000/svg">
<title id="ob-title">The transactional outbox</title>
<desc id="ob-desc">A transaction writes the business change and an outbox row together; after commit, a relay polls pending rows and publishes them, and consumers dedupe on the row id.</desc>
<defs>
<marker id="ob-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ob-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ob-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ob-arw)"/>
<text x="210" y="42" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">COMMIT</text>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Transaction</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">write + outbox row</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Relay</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">polls pending</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Publish</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">to the bus</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Consumer</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">dedupe on id</text>
</svg>
<figcaption>The event is published only after its transaction commits; the relay retries until each row is sent.</figcaption>
</figure>

## Setup

Wire the outbox with `For()`, which registers a GORM-backed outbox table plus the relay. If you provide your own `repomod.Repo[outboxmod.Record]`, use `Register()` instead.

```go
var _ = outboxmod.For()
```

Migrate the table alongside your other models.

```go
db.AutoMigrate(&outboxmod.Record{})
```

## Enqueuing inside a transaction

Inject `outboxmod.Outbox` and call `Enqueue` inside the same `repo.Tx` as your business write, passing the transactional context. Because the outbox row and the business change share one transaction, they commit or roll back together.

```go
type Orders struct {
    Repo   repomod.Repo[Order]  // injected
    Outbox outboxmod.Outbox     // injected
}

func (s *Orders) Place(ctx context.Context, o Order) error {
    return s.Repo.Tx(ctx, func(ctx context.Context) error {
        if err := s.Repo.Create(ctx, &o); err != nil {
            return err
        }
        return s.Outbox.Enqueue(ctx, "orders.placed", encode(o), nil)
    })
}
```

## The relay

The relay is a background service that polls for rows whose `PublishedAt` is null, publishes each through `eventmod.Publisher`, and stamps it published. A publish failure is left for the next tick, so an event is retried until it is sent. The poll interval and batch size are configurable.

```go
registry.RegisterInstance[*outboxmod.Options](app.Reg, &outboxmod.Options{
    Poll:  time.Second,
    Batch: 100,
})
```

## Idempotency

Because the relay may publish a row more than once (for example, if it crashes after publishing but before stamping the row), delivery is at-least-once. The relay sets the outbox row id on each message as the `Bosun-Outbox-Id` header, so a consumer can record processed ids and skip duplicates. This is the same idempotency requirement as the rest of the [event system](./events.md), and it is a hard requirement, not an optimization.
