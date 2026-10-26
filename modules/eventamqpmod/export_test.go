package eventamqpmod

// DropConnection closes the broker connection out from under the bus, as a
// broker restart would, so tests can exercise the reconnect supervisor.
func (b *Bus) DropConnection() {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	_ = conn.Close()
}
