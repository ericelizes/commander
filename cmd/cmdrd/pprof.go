package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on http.DefaultServeMux
	"os"
)

// startProfiler serves Go profiles when COMMANDER_PPROF_ADDR names a loopback
// address (for example 127.0.0.1:6061). Off by default.
func startProfiler() {
	addr := os.Getenv("COMMANDER_PPROF_ADDR")
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		log.Printf("cmdrd: profiler not started: %q is not a loopback address", addr)
		return
	}
	go func() { log.Printf("cmdrd: profiler stopped: %v", http.ListenAndServe(addr, nil)) }()
	log.Printf("cmdrd: profiler on http://%s/debug/pprof/", addr)
}
