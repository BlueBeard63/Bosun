module github.com/amberstack/bosun/modules/eventnatsmod

go 1.25.0

require (
	github.com/amberstack/bosun v0.0.0
	github.com/nats-io/nats.go v1.38.0
)

require (
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/nats-io/nkeys v0.4.9 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/amberstack/bosun => ../..
