package webshop

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Message is one letter.
type Message struct {
	To      string
	Subject string
	Body    string
}

// SendMail delivers m with cfg. The context bounds the dial.
func SendMail(ctx context.Context, cfg ShopConfig, m Message) error {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	host := cfg.SMTPHost
	var client *smtp.Client
	if cfg.SMTPTLS == "tls" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		client, err = smtp.NewClient(tlsConn, host)
	} else {
		client, err = smtp.NewClient(conn, host)
	}
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.SMTPTLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	if cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.SMTPFrom); err != nil {
		return err
	}
	if err := client.Rcpt(m.To); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	body := strings.ReplaceAll(m.Body, "\n", "\r\n")
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		cfg.SMTPFrom, m.To, m.Subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
