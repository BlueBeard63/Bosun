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
    if err := app.Run(":8080"); err != nil {
        log.Fatal(err)
    }
}
```

## The drivers

| Driver | Subject wildcards | Consumer groups | Request/reply | Notes |
| --- | --- | --- | --- | --- |
| **in-memory** (`eventmemmod`) | `*`, `>` | yes | yes | The default; no durability, so undelivered messages are lost on shutdown. |
| **RabbitMQ** (`eventamqpmod`) | `*`, `>` (mapped to `#`) | yes | yes | Topic exchange; durable queues for groups. |
| **NATS** (`eventnatsmod`) | `*`, `>` | yes | yes | Core NATS; back it with JetStream for durability. |
| **Redis Streams** (`eventredismod`) | none (exact subjects) | yes | no | `XADD` / `XREADGROUP` / `XACK`; streams are untrimmed unless `Options.MaxLen` is set. |

## RabbitMQ patterns

The RabbitMQ driver expresses the classic messaging patterns through the generic API rather than exposing broker-specific calls.

**Publish/Subscribe** delivers a copy to every consumer: subscribe without a group, and each subscriber gets its own queue bound to the subject. **Work Queues** load-balance across instances: use `WithGroup("workers")` so members share one durable queue per subject, named `<group>.<subject>` (for example `workers.orders.placed`), so one group consuming several subjects keeps each subject's messages with its own handler. **Topics** route by pattern: publish to a dotted subject such as `orders.eu.created` and subscribe with wildcards such as `orders.*.created` or `orders.>`. **Routing** is exact-subject topic matching. **Request/reply (RPC)** is covered on the [event RPC](./event-rpc.md) page.

The exchange type defaults to `topic`, which covers all of the above. Set `Options.Kind` to `direct` for exact-only routing or `fanout` for a broadcast exchange where the routing key is ignored.

`Publish` uses publisher confirms: it returns only after the broker has accepted the message, and returns `eventamqpmod.ErrNacked` if the broker rejects it. By default a message that no queue is bound to is dropped by the broker, as AMQP specifies; set `Options.Mandatory` to have `Publish` return `eventamqpmod.ErrUnroutable` instead.

```go
registry.RegisterInstance[*eventamqpmod.Options](app.Reg, &eventamqpmod.Options{
    URL:       "amqp://guest:guest@rabbit:5672/",
    Exchange:  "app.events",
    Kind:      "topic", // topic | direct | fanout
    Prefetch:  32,
    Mandatory: true, // fail Publish when nothing is bound to the subject
})
```

## Delivery and redelivery

On a handler error (a nack with requeue) the message is redelivered to the subscriber or consumer group that failed, and only to it: other subscribers and groups on the same subject do not receive it again.

- **RabbitMQ** puts a copy with an incremented `Attempt` on the subscription's own queue and acks the original only after the broker confirms the copy. If the copy cannot be published, the original is returned to the broker and the nack returns the error, so the message is not lost.
- **Redis** leaves the entry pending in the group and claims it back, so `Attempt` is the entry's delivery count. The stream itself is not touched. An entry held by a consumer that stopped without acking it (for example, a crashed replica) is claimed and redelivered by another member of the group once it has been idle for `Options.ClaimIdle` (30 seconds by default). A handler that runs longer than `ClaimIdle` may therefore see its message handled twice.
- **In-memory** re-enqueues the message with an incremented `Attempt`.
- **NATS** re-publishes the message with an incremented `Attempt` to a retry subject that only the failing subscription, or the members of its queue group, listen on. Core NATS stores nothing, so this is best effort: a message that is being handled when the process stops is not redelivered. Back the subject with JetStream if you need durable delivery.

Every driver's `Attempt` counts the deliveries of a message to that subscription, which is what the typed layer's attempt limit and dead-lettering use (see [retries](./events.md#retries-backoff-and-dead-lettering)). The backoff before a redelivery is applied as follows:

- **In-memory** re-enqueues the message from a timer once the delay has passed, so the subscription's workers keep handling other messages in the meantime. A message still waiting when the bus closes is lost, like any undelivered in-memory message.
- **RabbitMQ, NATS, and Redis** wait in the handler before nacking. The message stays unacked (RabbitMQ, Redis) or unpublished to the retry subject (NATS) while it waits, and the subscription dispatches nothing else until the wait ends, so the delay is bounded by the backoff's maximum. A handler that is backing off counts as in flight, so closing the bus can take up to that maximum longer. On Redis, keep that maximum well below `Options.ClaimIdle`, or another member of the group may claim the entry and handle it concurrently. On NATS, a message waiting out its backoff when the process stops is not redelivered, in keeping with core NATS being best effort.

The typed layer does its own dead-lettering by publishing to the subject you name with `WithDeadLetter`, so it works the same on every backend and needs no broker configuration. It is independent of a RabbitMQ queue's dead-letter exchange, which only receives messages that are rejected without requeue (for example, an undecodable payload when no dead-letter subject is set).

Redis streams grow without limit by default. Set `Options.MaxLen` to trim each stream to roughly that many entries on every publish (`XADD MAXLEN ~`), and size it for your slowest group, because entries trimmed before a group reads them are lost to that group.

```go
registry.RegisterInstance[*eventredismod.Options](app.Reg, &eventredismod.Options{
    Addr:      "redis:6379",
    ClaimIdle: time.Minute, // reclaim entries a dead consumer left unacked
    MaxLen:    100_000,     // keep roughly the last 100k entries per stream
})
```

RabbitMQ, Redis, and the in-memory bus are at-least-once. Because redelivery is always possible, handlers must be idempotent, as described on the [events](./events.md) page.
