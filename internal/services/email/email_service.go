// Spec: specs/042-modulo-eventos/spec.md
//
// Package email is VendIA's first outbound email sender. It backs the event
// reminders (Spec F042 FR-20). It uses the stdlib net/smtp so it adds no
// dependency; when SMTP is not configured it degrades to a FakeSender that
// records messages (mirrors the push FakeSender pattern) so the rest of the
// system keeps working in dev and tests.
package email

import (
	"context"
	"fmt"
	"log"
	"net/smtp"
	"strings"
)

// Message is one outbound email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers a Message. Implementations: SMTPSender (prod) and
// FakeSender (dev/tests/no-config).
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// FakeSender records messages instead of sending them.
type FakeSender struct {
	Sent []Message
}

// Send appends the message to the in-memory log.
func (f *FakeSender) Send(_ context.Context, msg Message) error {
	f.Sent = append(f.Sent, msg)
	log.Printf("[EMAIL:fake] to=%s subject=%q", msg.To, msg.Subject)
	return nil
}

// SMTPSender delivers via a plain SMTP server (host:port, PLAIN auth).
type SMTPSender struct {
	addr string // host:port
	auth smtp.Auth
	from string
}

// Send delivers the message over SMTP.
func (s *SMTPSender) Send(_ context.Context, msg Message) error {
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n",
		s.from, msg.To, msg.Subject)
	payload := []byte(headers + msg.Body)
	return smtp.SendMail(s.addr, s.auth, s.from, []string{msg.To}, payload)
}

// Config carries the SMTP settings (all from env at the call site).
type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// Service builds and dispatches the event-related emails in Spanish (Art. V).
type Service struct {
	sender     Sender
	from       string
	configured bool
}

// NewService picks an SMTP sender when a host is configured, else a FakeSender.
func NewService(cfg Config) *Service {
	if strings.TrimSpace(cfg.Host) == "" {
		return &Service{sender: &FakeSender{}, from: cfg.From, configured: false}
	}
	port := cfg.Port
	if port == "" {
		port = "587"
	}
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	return &Service{
		sender:     &SMTPSender{addr: cfg.Host + ":" + port, auth: smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host), from: from},
		from:       from,
		configured: true,
	}
}

// NewServiceWithSender injects a sender (used by tests).
func NewServiceWithSender(sender Sender, from string) *Service {
	return &Service{sender: sender, from: from, configured: true}
}

// IsConfigured reports whether a real SMTP sender is wired.
func (s *Service) IsConfigured() bool { return s.configured }

// QuotaReminder is the data for a pending-installment reminder.
type QuotaReminder struct {
	To, Name, EventTitle, AmountStr, DueDateStr string
	// Link al catálogo con el token del inscrito (?reg=) para pagar y ver su
	// carné quedando "logueado". Opcional.
	Link string
}

// EventReminder is the data for an upcoming-event reminder.
type EventReminder struct {
	To, Name, EventTitle, WhenStr string
	// Link al catálogo con el token del inscrito (?reg=). Opcional.
	Link string
}

func reminderLinkLine(link string) string {
	if link == "" {
		return ""
	}
	return fmt.Sprintf("\n\nVer los detalles y tu carné aquí:\n%s", link)
}

// SendQuotaReminder emails an attendee about a pending balance.
func (s *Service) SendQuotaReminder(ctx context.Context, r QuotaReminder) error {
	if r.To == "" {
		return nil
	}
	subject := fmt.Sprintf("Recordatorio de pago — %s", r.EventTitle)
	body := fmt.Sprintf(
		"Hola %s,\n\nTe recordamos que tienes un saldo pendiente de %s para el evento \"%s\". Completa tu pago para activar tu carné.%s\n\nGracias.",
		r.Name, r.AmountStr, r.EventTitle, reminderLinkLine(r.Link))
	return s.sender.Send(ctx, Message{To: r.To, Subject: subject, Body: body})
}

// SendEventReminder emails an attendee about an upcoming event.
func (s *Service) SendEventReminder(ctx context.Context, r EventReminder) error {
	if r.To == "" {
		return nil
	}
	subject := fmt.Sprintf("Tu evento se acerca — %s", r.EventTitle)
	body := fmt.Sprintf(
		"Hola %s,\n\nTe recordamos que el evento \"%s\" será %s. ¡Te esperamos!%s\n\nGracias.",
		r.Name, r.EventTitle, r.WhenStr, reminderLinkLine(r.Link))
	return s.sender.Send(ctx, Message{To: r.To, Subject: subject, Body: body})
}
