# Correlation and tracing

Bosun gives every request a correlation id and can emit distributed traces, and it carries both across HTTP and the event bus so one logical operation is followed end to end. Correlation is built in and dependency-free; tracing is an extension point with a no-op default and an OpenTelemetry driver, so the heavy tracing dependency never touches core.

<figure class="diagram">
<svg viewBox="0 0 720 210" role="img" aria-labelledby="tr-title tr-desc" xmlns="http://www.w3.org/2000/svg">
<title id="tr-title">A trace travels across HTTP and the event bus</title>
<desc id="tr-desc">An inbound traceparent seeds an HTTP span; the traceparent rides the context and event headers so a consumer opens a child span in the same trace.</desc>
<defs>
<marker id="tr-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="60" x2="192" y2="60" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#tr-arw)"/>
<line x1="346" y1="60" x2="366" y2="60" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#tr-arw)"/>
<line x1="520" y1="60" x2="540" y2="60" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#tr-arw)"/>
<line x1="95" y1="88" x2="95" y2="130" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<line x1="269" y1="88" x2="269" y2="130" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<line x1="443" y1="88" x2="443" y2="130" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<line x1="617" y1="88" x2="617" y2="130" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<rect x="20" y="32" width="150" height="56" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="56" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">HTTP request</text>
<text x="95" y="73" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">traceparent in</text>
<rect x="194" y="32" width="150" height="56" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="269" y="56" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Tracing MW</text>
<text x="269" y="73" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">starts span</text>
<rect x="368" y="32" width="150" height="56" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="443" y="56" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Publish</text>
<text x="443" y="73" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">event</text>
<rect x="542" y="32" width="150" height="56" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="56" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Consumer</text>
<text x="617" y="73" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">child span</text>
<rect x="20" y="130" width="672" height="40" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1" stroke-dasharray="4,3"/>
<text x="356" y="155" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="12" font-weight="600" fill="var(--fg)">trace context: W3C traceparent (on ctx and event headers)</text>
</svg>
<figcaption>The traceparent seeds the HTTP span, rides the context onto published events, and lets a consumer open a child span in the same trace.</figcaption>
</figure>

## Correlation ids

The `mw.Correlation` middleware ensures every request carries a correlation id: it reuses an inbound `X-Correlation-ID`, derives one from a `traceparent`, or mints a fresh id, then attaches it to the request context and echoes it on the response. The typed adapter copies the id onto every `AuditEvent`, and the event bus carries it across a publish, so a log line, an audit record, and a downstream consumer all share one id. Attach it early so everything downstream sees the same value.

```go
var _ = bosun.Controller[API]("/api", bosun.Use[mw.Correlation]())
```

Read it anywhere from the context with `bosun.CorrelationID(ctx)`.

## Distributed tracing

Tracing is defined by the `tracemod.Tracer` interface, which starts a span for an HTTP request or an event consumption. The default binding is a no-op, so tracing code paths are identical whether or not a real tracer is installed. Add the `tracemod.Tracing` middleware to open a span per request; with the no-op tracer it costs nothing.

```go
var _ = bosun.Controller[API]("/api",
    bosun.Use[mw.Correlation](),
    bosun.Use[tracemod.Tracing](),
)
```

## The OpenTelemetry driver

The `traceotelmod` driver implements `tracemod.Tracer` over OpenTelemetry, and it lives in its own module so the OTel dependency stays out of core. Configure your OTel `TracerProvider` and exporter in `main` as you normally would, then install the driver, which overrides the no-op tracer.

```go
func main() {
    // set up the OTel SDK: exporter + TracerProvider + otel.SetTracerProvider(...)
    app := bosun.New()
    traceotelmod.Install(app.Reg)
    log.Fatal(app.Run(":8080"))
}
```

## Propagation across the bus

A trace is continued through the W3C `traceparent`, which core Bosun carries on the context (`bosun.Traceparent`). The tracing middleware reads an inbound `traceparent` and writes the current one back onto the context; the event bus copies that value onto each published message and restores it on consume. A consumer that opens a span with `StartConsume` therefore joins the same trace that began at the HTTP request, without any manual plumbing. Because correlation and tracing share these context keys, the [event](./events.md) carrier propagates both together.
