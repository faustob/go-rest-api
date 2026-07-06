// ----------------------------------------------------------------------------
// Wires initAppMetrics() into the startup sequence after initOTel().
// This file exists solely to keep server.go edits minimal.
// ----------------------------------------------------------------------------

package main

// initTelemetry is called from main() after the OTel SDK is registered.
// It initialises all application-level metric instruments.
func initTelemetry() {
	initAppMetrics()
}
