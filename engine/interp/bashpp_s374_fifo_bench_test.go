package interp_test

import "testing"

// BenchmarkBashPPFIFORegistryContention measures the statement path taken by
// several interpreted goroutines while the FIFO registry is empty. The body
// is deliberately all interpreted Go-source code: no native helper is used
// for the loop or its synchronization.
func BenchmarkBashPPFIFORegistryContention(b *testing.B) {
	const goroutines = 8
	const statementsPerGoroutine = 200
	const source = `package main
func main() {
	done := make(chan bool, 8)
	for worker := 0; worker < 8; worker++ {
		go func() {
			n := 0
			for step := 0; step < 200; step++ {
				n++
			}
			if n != 200 { panic("loop") }
			done <- true
		}()
	}
	for worker := 0; worker < 8; worker++ { <-done }
}`
	b.ReportMetric(goroutines, "goroutines/op")
	b.ReportMetric(goroutines*statementsPerGoroutine, "statements/op")
	benchGoSource(b, "s374-fifo-registry-contention.go", source)
}
