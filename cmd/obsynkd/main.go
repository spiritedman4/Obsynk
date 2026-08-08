// Command obsynkd is the Obsynk sync daemon: one instance per vault, reached
// by the Obsidian plugin over loopback-TCP gRPC. Lifecycle is tied to the
// plugin session (spawned on load, stopped via the Shutdown RPC or a
// termination signal on unload).
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	obsynkv1 "obsynk/gen/go/obsynk/v1"
	"obsynk/internal/grpcserver"
)

const version = "0.0.1-dev"

func main() {
	vaultPath := flag.String("vault", "", "absolute path to the Obsidian vault to sync (required)")
	flag.Parse()

	if *vaultPath == "" {
		log.Fatal("obsynkd: -vault is required")
	}

	absVault, err := filepath.Abs(*vaultPath)
	if err != nil {
		log.Fatalf("obsynkd: resolving vault path: %v", err)
	}

	// Persist logs to a file in addition to stderr: stderr only reaches
	// Obsidian's dev console if it happened to be open (and forwarded by
	// the plugin) at the moment something went wrong; a file survives
	// across restarts and doesn't require the console to have been open.
	dataDir := filepath.Join(absVault, ".obsidian", "plugins", "obsynk")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("obsynkd: creating data dir %s: %v", dataDir, err)
	}
	logFile, err := os.OpenFile(filepath.Join(dataDir, "obsynkd.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatalf("obsynkd: opening log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, logFile))

	lis, err := grpcserver.Listen()
	if err != nil {
		log.Fatalf("obsynkd: binding loopback listener: %v", err)
	}
	defer lis.Close()

	srv, err := grpcserver.NewServer(version, absVault)
	if err != nil {
		log.Fatalf("obsynkd: initializing server: %v", err)
	}
	grpcSrv := grpc.NewServer()
	obsynkv1.RegisterObsynkServiceServer(grpcSrv, srv)

	port := lis.Addr().(*net.TCPAddr).Port
	// Parsed by the Obsidian plugin after spawning this process to discover
	// which port to connect to; keep this the first/only stdout line with
	// this prefix.
	fmt.Printf("OBSYNKD_ADDR=127.0.0.1:%d\n", port)
	log.Printf("obsynkd: vault=%s listening on 127.0.0.1:%d", absVault, port)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- grpcSrv.Serve(lis)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serveErrCh:
		if err != nil {
			log.Fatalf("obsynkd: serve error: %v", err)
		}
	case <-sigCh:
		log.Print("obsynkd: signal received, stopping")
	case <-srv.StopRequested():
		log.Print("obsynkd: Shutdown RPC received, stopping")
	}

	grpcSrv.GracefulStop()
	log.Print("obsynkd: stopped")
}
