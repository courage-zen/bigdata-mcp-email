package main

import (
	"log"
	"net/http"
	"net/url"
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
	if cfg.S3 != nil {
		// TEMPORARY (Task 3 deletes this): until NewMinioObjectStore is
		// rewritten to take S3Config, strip AWS_ENDPOINT_URL's scheme here so
		// the standard config form doesn't die at minio.New("") with a
		// misleading endpoint error. Legacy S3_ENDPOINT is already bare.
		endpoint, secure := cfg.S3.Endpoint, cfg.S3.Secure
		if cfg.S3.EndpointURL != "" {
			u, err := url.Parse(cfg.S3.EndpointURL)
			if err != nil {
				log.Fatalf("s3: invalid AWS_ENDPOINT_URL: %v", err)
			}
			endpoint, secure = u.Host, u.Scheme == "https"
		}
		store, err := NewMinioObjectStore(endpoint, cfg.S3.AccessKeyID, cfg.S3.SecretKey, cfg.S3.Bucket, secure)
		if err != nil {
			log.Fatalf("s3: %v", err)
		}
		env.Store = store
	}
	addr := host + ":" + strconv.Itoa(port)
	s3State := "off"
	if env.Store != nil {
		s3State = cfg.S3.Bucket
	}
	log.Printf("email-mcp listening on %s (smtp %s:%d, from %s, s3 %s)", addr, cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.From, s3State)
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
