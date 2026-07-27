// Command gateway is the Ollama embeddings proxy: it authenticates API keys,
// enforces per-token batch/rate/budget limits, forwards accepted requests to the
// Caddy load balancer, and serves the usage dashboard.
//
// This file is the composition root; it is fleshed out once the packages it
// wires together exist (see Task 8).
package main

import "fmt"

func main() {
	// Placeholder wiring — replaced by the real composition root in Task 8.
	fmt.Println("ragembedingv1 gateway")
}
