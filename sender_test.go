package main

import (
	"bufio"
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSMTPServer accepts one connection, records envelope and DATA bytes.
type fakeSMTPServer struct {
	ln       net.Listener
	MailFrom string
	RcptTo   []string
	Data     []byte
	ErrCh    chan error
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTPServer{ln: ln, ErrCh: make(chan error, 1)}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSMTPServer) addr() string {
	return s.ln.Addr().String()
}

func (s *fakeSMTPServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		s.ErrCh <- err
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	r := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")

	readLine := func() string {
		line, err := r.ReadString('\n')
		if err != nil {
			return ""
		}
		return strings.TrimRight(line, "\r\n")
	}
	inData := false
	var data bytes.Buffer
	for {
		line := readLine()
		if line == "" && !inData {
			break
		}
		if inData {
			if line == "." {
				s.Data = data.Bytes()
				write("250 OK")
				inData = false
				continue
			}
			// Unescape leading dot per RFC 5321 §4.5.2.
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			data.WriteString(line)
			data.WriteString("\n")
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250-fake")
			write("250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			// Line is "MAIL FROM:<addr>" or "MAIL FROM:<addr> BODY=8BITMIME"
			// (Go's smtp client appends BODY=8BITMIME because we advertise it).
			// Extract just the bracketed address.
			rest := strings.TrimPrefix(line, "MAIL FROM:")
			if i := strings.IndexByte(rest, '>'); i >= 0 {
				rest = rest[:i+1]
			}
			s.MailFrom = strings.Trim(rest, "<>")
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			rcpt := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(line, "RCPT TO:"), ">"), " <>")
			s.RcptTo = append(s.RcptTo, rcpt)
			write("250 OK")
		case upper == "DATA":
			write("354 go ahead")
			inData = true
		case upper == "QUIT":
			write("221 bye")
			s.ErrCh <- nil
			return
		default:
			write("250 OK")
		}
	}
	s.ErrCh <- nil
}

func TestSenderSend_Plain(t *testing.T) {
	srv := newFakeSMTPServer(t)
	host, portStr, _ := net.SplitHostPort(srv.addr())
	port, _ := strconv.Atoi(portStr)

	sender := &SMTPSender{Host: host, Port: port}
	msg := []byte("From: noreply@example.com\r\nTo: a@x.com\r\nSubject: s\r\n\r\nhello\r\n")
	if err := sender.Send("noreply@example.com", []string{"a@x.com"}, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-srv.ErrCh:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake server did not finish")
	}
	if srv.MailFrom != "noreply@example.com" {
		t.Errorf("MAIL FROM = %q", srv.MailFrom)
	}
	if len(srv.RcptTo) != 1 || srv.RcptTo[0] != "a@x.com" {
		t.Errorf("RCPT TO = %v", srv.RcptTo)
	}
	if !bytes.Contains(srv.Data, []byte("Subject: s")) {
		t.Errorf("DATA lost subject:\n%s", srv.Data)
	}
}

func TestSenderSend_MultipleRcpt(t *testing.T) {
	srv := newFakeSMTPServer(t)
	host, portStr, _ := net.SplitHostPort(srv.addr())
	port, _ := strconv.Atoi(portStr)

	sender := &SMTPSender{Host: host, Port: port}
	msg := []byte("Subject: s\r\n\r\nx\r\n")
	if err := sender.Send("noreply@example.com", []string{"a@x.com", "b@x.com"}, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-srv.ErrCh:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake server did not finish")
	}
	if len(srv.RcptTo) != 2 {
		t.Errorf("RCPT TO = %v, want 2 recipients", srv.RcptTo)
	}
}

func TestSenderSend_Unreachable(t *testing.T) {
	// Bind then close to reserve an unused loopback port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	sender := &SMTPSender{Host: host, Port: port}
	err = sender.Send("noreply@example.com", []string{"a@x.com"}, []byte("x"))
	if err == nil {
		t.Fatal("expected dial error")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Errorf("error should mention dial stage: %v", err)
	}
}
