//go:build windows

package claudeadapter

// recordAPIKeyDelivery is a no-op on Windows, where the key stays in the
// CLI environment.
func recordAPIKeyDelivery(string) {}
