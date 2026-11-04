package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	cfg, err := LoadConfig(os.Getenv("EMAIL_MCP_CONFIG"))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	authKeys := os.Getenv("MCP_AUTH_KEY")
	host := os.Getenv("MCP_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port := 8000
	if v := os.Getenv("MCP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		} else {
			log.Fatalf("MCP_PORT: %v", err)
		}
	}

	env := &ServerEnv{Config: cfg, Sender: &SMTPSender{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port}}
	addr := host + ":" + strconv.Itoa(port)
	log.Printf("email-mcp listening on %s (smtp %s:%d, from %s)", addr, cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.From)
	// ReadHeaderTimeout is the slowloris guard: the server binds 0.0.0.0 by
	// default, so slow-header clients must not be able to pin connections
	// open forever. Everything else is deliberately the zero value, matching
	// ListenAndServe's behavior.
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewHandler(env, authKeys),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
