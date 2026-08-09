package main

import "testing"

// main() treats a failure from NewLocalNotifier as non-fatal and keeps running
// without a notifier, but it still runs `defer notifier.Close()`. That deferred
// call landed on a nil receiver and panicked with "invalid memory address or
// nil pointer dereference" at local_notifier.go:63, taking down the process at
// the end of every cycle where notifier setup had failed.
func TestLocalNotifierCloseOnNil(t *testing.T) {
	var n *LocalNotifier
	n.Close()
}

// A notifier that was constructed but never opened its files must not panic
// either - the fields are set one at a time in NewLocalNotifier, so an early
// error leaves a partially built value behind.
func TestLocalNotifierCloseWithNoFiles(t *testing.T) {
	(&LocalNotifier{}).Close()
}
