# Webhooks

The webhook receiver accepts inbound HTTP callbacks from external providers such as GitHub, Stripe, or a git server, verifies their signatures, and dispatches them to handlers you register per provider and event type. It mounts one route per provider under a configurable path, so all of your integrations share a single, verified entry point.

<figure class="diagram">
<svg viewBox="0 0 720 170" role="img" aria-labelledby="wh-title wh-desc" xmlns="http://www.w3.org/2000/svg">
<title id="wh-title">Inbound webhook flow</title>
<desc id="wh-desc">A provider posts a signed request; the receiver verifies the signature and dispatches to handlers registered for the event type, rejecting a bad signature with 401.</desc>
<defs>
<marker id="wh-arw" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto"><polygon points="0 0, 8 3, 0 6" fill="var(--fg-muted)"/></marker>
</defs>
<line x1="172" y1="64" x2="192" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#wh-arw)"/>
<line x1="346" y1="64" x2="366" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#wh-arw)"/>
<line x1="520" y1="64" x2="540" y2="64" stroke="var(--fg-muted)" stroke-width="1" marker-end="url(#wh-arw)"/>
<text x="443" y="42" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="8" letter-spacing="0.06em" fill="var(--fg-muted)">401 ON MISMATCH</text>
<rect x="20" y="32" width="150" height="64" rx="6" fill="var(--code-bg)" stroke="var(--fg-muted)" stroke-width="1"/>
<text x="95" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Provider</text>
<text x="95" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">signed POST</text>
<rect x="194" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="269" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Receiver</text>
<text x="269" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">/webhooks/{p}</text>
<rect x="368" y="32" width="150" height="64" rx="6" fill="var(--accent-soft)" stroke="var(--accent)" stroke-width="1"/>
<text x="443" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--accent)">Verify</text>
<text x="443" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">HMAC / Stripe</text>
<rect x="542" y="32" width="150" height="64" rx="6" fill="var(--bg)" stroke="var(--fg)" stroke-width="1"/>
<text x="617" y="60" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="13" font-weight="600" fill="var(--fg)">Handlers</text>
<text x="617" y="78" text-anchor="middle" font-family="'JetBrains Mono',ui-monospace,monospace" font-size="9" fill="var(--fg-muted)">by event type</text>
</svg>
<figcaption>A request with an invalid signature is rejected with 401 before any handler runs.</figcaption>
</figure>

## Registering a handler

Register a handler for a provider and event type. Use `"*"` as the event type to receive every event from a provider. A common use is turning a git push into a deploy, or a payment event into a fulfillment.

```go
var _ = webhookmod.Handle("github", "push", func(ctx context.Context, e webhookmod.Event) error {
    return deploy(ctx, e.Payload)
})
```

Each `Event` carries the provider, the event type, a delivery id (for dedupe), the raw payload, and the request headers.

## Verifying signatures

Configure a verifier per provider so unsigned or forged requests are rejected before any handler sees them. The receiver ships verifiers for GitHub and Stripe, plus a generic HMAC verifier for your own services; a provider with no verifier is accepted as-is, which you should use only behind a trusted proxy.

```go
func main() {
    app := bosun.New()
    registry.RegisterInstance[*webhookmod.Options](app.Reg, &webhookmod.Options{
        Path: "/webhooks",
        Verifiers: map[string]webhookmod.Verifier{
            "github": webhookmod.GitHubVerifier(os.Getenv("GH_WEBHOOK_SECRET")),
            "stripe": webhookmod.StripeVerifier{Secret: os.Getenv("STRIPE_SECRET")},
        },
    })
    log.Fatal(app.Run(":8080"))
}
```

The GitHub verifier checks the `X-Hub-Signature-256` HMAC, the Stripe verifier checks the timestamped `Stripe-Signature` scheme, and the generic `HMACVerifier` checks a hex HMAC-SHA256 of the body against a header you name.

## Reliability

A webhook handler runs inline during the request, so a slow handler slows the provider's delivery and a failure returns an error to the provider, which usually retries. For work that must not be lost or that fans out to other services, have the handler write to the [outbox](./outbox.md) inside a transaction and return quickly; the relay then publishes the event reliably.
