// Package a is the one mapped dependency of the Sprint 151 M1 spike fixture.
// It is registered under the explicit import path "test/a" (import base
// "test"), the convention packages_test.go uses; it is never looked up on disk.
package a

// Greeting is the single exported function main calls through the map.
func Greeting(name string) string { return "hello, " + name }
