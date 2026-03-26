// Package main is the entry point for the oberwatch binary.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// TODO: Initialize CLI with cobra, config, and subcommand routing.
	return nil
}
