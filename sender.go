package main

import (
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPSender submits mail to a plaintext SMTP endpoint (local-smtp-forwarder).
// No TLS, no AUTH: it trusts the network to be loopback.
type SMTPSender struct {
	Host string
	Port int
}

// Send performs one SMTP transaction: EHLO, MAIL FROM, RCPT TO (each
// recipient), DATA, QUIT. Errors are wrapped with the stage that failed.
func (s *SMTPSender) Send(from string, to []string, msg []byte) error {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return fmt.Errorf("set deadline: %w", err)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if err := c.Hello("email-mcp"); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO <%s>: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish DATA: %w", err)
	}
	return c.Quit()
}
