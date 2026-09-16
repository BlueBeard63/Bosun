# Event backends

The event API is driver-agnostic: producers and consumers depend on the `eventmod` interfaces, and a driver binds those interfaces to a concrete transport. You select a backend by importing its module and calling `Use()`; because the binding yields to host registrations, an app can still override it. This page lists the drivers, shows how to select one, and maps each to the messaging patterns.

<figure class="diagram">
<svg viewBox="0 0 660 250" role="img" aria-labelledby="eb-title eb-desc" xmlns="http://www.w3.org/2000/svg">
<title id="eb-title">One interface, swappable drivers</title>
<desc id="eb-desc">The eventmod bus interface is bound by DefaultBind to one of the in-memory, RabbitMQ, NATS, or Redis drivers.</desc>
<defs>
<marker id="eb-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<text x="295" y="30" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.1em" fill="var(--fg-muted)">DEFAULTBIND (HOST WINS)</text>
<line x1="212" y1="60" x2="378" y2="60" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#eb-arw)"/>
<line x1="212" y1="108" x2="378" y2="108" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#eb-arw)"/>
<line x1="212" y1="156" x2="378" y2="156" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#eb-arw)"/>
<line x1="212" y1="204" x2="378" y2="204" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#eb-arw)"/>
<rect x="20" y="40" width="192" height="176" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="116" y="124" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="14" font-weight="600" fill="var(--accent)">eventmod.Bus</text>
<text x="116" y="144" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">Publisher + Subscriber</text>
<rect x="380" y="44" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="64" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">eventmemmod (in-memory, default)</text>
<rect x="380" y="92" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="112" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">eventamqpmod (RabbitMQ)</text>
<rect x="380" y="140" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="160" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">eventnatsmod (NATS)</text>
<rect x="380" y="188" width="280" height="32" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="396" y="208" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">eventredismod (Redis Streams)</text>
</svg>
<figcaption>Producers and consumers never change; importing a driver and calling Use() rebinds the interface.</figcaption>
</figure>

## Selecting a backend

Import the driver package for its registration side effect and call `Use()` at init. To point it at your server, register its options.

```go
import _ "github.com/bluebeard63/bosun/modules/eventamqpmod"

var _ = eventamqpmod.Use()
```

```go
func main() {
    app := bosun.New()
    registry.RegisterInstance[*eventamqpmod.Options](app.Reg, &eventamqpmod.Options{
        URL: "amqp://guest:guest@rabbit:5672/",
    })
    log.Fatal(app.Run(":8080"))
}
```

## The drivers

| Driver | Subject wildcards | Consumer groups | Request/reply | Notes |
| --- | --- | --- | --- | --- |
| **in-memory** (`eventmemmod`) | `*`, `>` | yes | yes | The default; no durability, so undelivered messages are lost on shutdown. |
| **RabbitMQ** (`eventamqpmod`) | `*`, `>` (mapped to `#`) | yes | yes | Topic exchange; durable queues for groups. |
| **NATS** (`eventnatsmod`) | `*`, `>` | yes | yes | Core NATS; back it with JetStream for durability. |
| **Redis Streams** (`eventredismod`) | none (exact subjects) | yes | no | `XADD` / `XREADGROUP` / `XACK`. |

## RabbitMQ patterns

The RabbitMQ driver expresses the classic messaging patterns through the generic API rather than exposing broker-specific calls.

**Publish/Subscribe** delivers a copy to every consumer: subscribe without a group, and each subscriber gets its own queue bound to the subject. **Work Queues** load-balance across instances: use `WithGroup("workers")` so members share one durable queue. **Topics** route by pattern: publish to a dotted subject such as `orders.eu.created` and subscribe with wildcards such as `orders.*.created` or `orders.>`. **Routing** is exact-subject topic matching. **Request/reply (RPC)** is covered on the [event RPC](./event-rpc.md) page.

The exchange type defaults to `topic`, which covers all of the above. Set `Options.Kind` to `direct` for exact-only routing or `fanout` for a broadcast exchange where the routing key is ignored.

```go
registry.RegisterInstance[*eventamqpmod.Options](app.Reg, &eventamqpmod.Options{
    URL:      "amqp://guest:guest@rabbit:5672/",
    Exchange: "app.events",
    Kind:     "topic", // topic | direct | fanout
    Prefetch: 32,
})
```

## Delivery and redelivery

Every driver provides at-least-once delivery. On a handler error the message is redelivered: RabbitMQ and the in-memory bus re-enqueue with an incremented `Attempt`, NATS re-publishes, and Redis leaves the entry pending until it is re-added. Because redelivery is always possible, handlers must be idempotent, as described on the [events](./events.md) page.
