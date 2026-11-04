package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
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
	log.Fatal(http.ListenAndServe(addr, NewHandler(env, authKeys)))
}
