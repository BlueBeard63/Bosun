# Events

Bosun's event system is asynchronous publish/subscribe messaging. A service publishes a message to a subject without knowing who consumes it, and any number of services subscribe to subjects they care about. This decouples services in time and in deployment, which is what makes it the backbone for a microservice architecture where each service is its own Bosun app. This page covers publishing, subscribing, delivery guarantees, and consumer groups; the [backends](./event-backends.md) and [request/reply](./event-rpc.md) pages cover driver selection and RPC.

<figure class="diagram">
<svg viewBox="0 0 720 260" role="img" aria-labelledby="ev-title ev-desc" xmlns="http://www.w3.org/2000/svg">
<title id="ev-title">Publish, fan-out, and consumer groups</title>
<desc id="ev-desc">A publisher sends to a subject on the bus; a plain subscriber receives every message, while members of a consumer group share the messages, one per member.</desc>
<defs>
<marker id="ev-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="150" x2="248" y2="150" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ev-arw)"/>
<line x1="402" y1="90" x2="516" y2="90" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ev-arw)"/>
<line x1="402" y1="195" x2="496" y2="195" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#ev-arw)"/>
<text x="210" y="142" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">PUBLISH</text>
<text x="459" y="82" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">COPY</text>
<text x="449" y="187" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">ONE OF</text>
<rect x="20" y="120" width="152" height="60" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="96" y="146" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Publisher</text>
<text x="96" y="164" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">emits events</text>
<rect x="250" y="60" width="152" height="180" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="326" y="146" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Bus</text>
<text x="326" y="164" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">subject</text>
<rect x="518" y="64" width="182" height="52" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="609" y="86" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Subscriber</text>
<text x="609" y="103" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">gets every message</text>
<rect x="498" y="150" width="202" height="92" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<text x="514" y="170" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.12em" fill="var(--fg-muted)">GROUP: WORKERS</text>
<rect x="514" y="178" width="170" height="26" rx="5" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="599" y="195" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">Worker 1</text>
<rect x="514" y="208" width="170" height="26" rx="5" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="599" y="225" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">Worker 2</text>
</svg>
<figcaption>A plain subscriber gets a copy of every message; a consumer group shares the messages, so each is handled by exactly one member.</figcaption>
</figure>

## Publishing and subscribing

The typed API is a `Topic[T]`, which JSON-encodes on publish and decodes on consume. A producer injects `eventmod.Publisher` and calls `Publish`; a consumer injects `eventmod.Subscriber` and registers a handler with `On`. Correlation and tenant metadata propagate automatically with each message.

```go
var UserCreated = eventmod.Topic[User]{Subject: "users.created"}

type Producer struct {
    Bus eventmod.Publisher // injected
}

func (p *Producer) Emit(ctx context.Context, u User) error {
    return UserCreated.Publish(ctx, p.Bus, u)
}
```

A consumer starts its subscription in `Init()` and closes it in `Close()`, which is the same background-service lifecycle used elsewhere in Bosun.

```go
type Mailer struct {
    Bus eventmod.Subscriber // injected
    sub eventmod.Subscription
}

func (m *Mailer) Init() error {
    s, err := UserCreated.On(context.Background(), m.Bus, func(ctx context.Context, u User) error {
        return m.sendWelcome(ctx, u)
    })
    m.sub = s
    return err
}

func (m *Mailer) Close() error { return m.sub.Unsubscribe() }
```

## At-least-once delivery and idempotency

Delivery is at-least-once across every backend: a message may be redelivered after a handler error or a consumer crash. Handlers must therefore be idempotent, meaning that processing the same message twice has the same effect as processing it once. Each delivery carries a `Message.ID` and a 1-based `Message.Attempt` so a handler can detect and skip a duplicate. Returning an error from a typed handler nacks the message so the backend redelivers it; returning nil acks it.

## Consumer groups

Without a group, every subscriber to a subject receives a copy of each message, which is the fan-out (publish/subscribe) pattern. With `eventmod.WithGroup("name")`, the members of that group compete for messages, so each message is handled by exactly one member, which is the work-queue pattern for load-balancing across instances.

```go
_, err := UserCreated.On(ctx, bus, handle, eventmod.WithGroup("mailers"))
```

## The default bus and swapping backends

If no broker driver is imported, the in-memory bus (`eventmemmod`) is bound automatically, so tests and single-process apps get a working bus with no setup. Importing a broker driver and calling its `Use()` swaps the backend for the whole app without changing any producer or consumer code. The [backends](./event-backends.md) page covers the in-memory, RabbitMQ, NATS, and Redis drivers and how each maps to the messaging patterns.
