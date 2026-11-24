package imap_test

import (
	"context"
	"testing"

	"github.com/zhecho/m9s/internal/config"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
)

func TestNewClient(t *testing.T) {
	cfg := &config.IMAPConfig{
		Host: "imap.example.com",
		Port: 993,
		TLS:  true,
		User: "user@example.com",
	}
	c := imapadapter.New(cfg)
	if c == nil {
		t.Fatal("New returned nil")
	}
}

func TestClientImplementsInterface(t *testing.T) {
	cfg := &config.IMAPConfig{Host: "localhost", Port: 993}
	var _ imapadapter.Client = imapadapter.New(cfg)
}

func TestBuildSearchCriteria(t *testing.T) {
	c := imapadapter.BuildSearchCriteria("hello world")
	if c == nil {
		t.Fatal("BuildSearchCriteria returned nil")
	}
}

func TestClient_status_notConnected(t *testing.T) {
	cfg := &config.IMAPConfig{Host: "localhost", Port: 993}
	c := imapadapter.New(cfg)
	_, err := c.Status(context.Background(), "INBOX")
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestClient_append_notConnected(t *testing.T) {
	cfg := &config.IMAPConfig{Host: "localhost", Port: 993}
	c := imapadapter.New(cfg)
	_, err := c.Append(context.Background(), "Drafts", []byte("From: me\r\n\r\nbody"), nil)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestClient_move_notConnected(t *testing.T) {
	cfg := &config.IMAPConfig{Host: "localhost", Port: 993}
	c := imapadapter.New(cfg)
	err := c.Move(context.Background(), "INBOX", 1, "Trash")
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestClient_expunge_notConnected(t *testing.T) {
	cfg := &config.IMAPConfig{Host: "localhost", Port: 993}
	c := imapadapter.New(cfg)
	err := c.Expunge(context.Background(), "Trash", 1)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}
