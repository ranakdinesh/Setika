package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"y/internal/app"
)

func main() {
	// 1. Initialize the App (Wiring)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application, err := app.New(ctx)
	if err != nil {
		fmt.Printf("Fatal: %v\n", err)
		os.Exit(1)
	}

	// 2. Setup Graceful Shutdown
	// We listen for interrupt signals to cancel the context

	defer cancel()

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c // Block until signal received
		cancel()
	}()

	// 3. Start the Engine
	// This blocks until the server shuts down
	if err := application.Start(ctx); err != nil {
		fmt.Printf("Runtime Error: %v\n", err)
		os.Exit(1)
	}
}
