package auth

import (
	"errors"
	"fmt"
	"net/smtp"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

// errSMTPUnset signals that no SMTP host is configured; callers fall back to
// logging the message content (links stay reachable on dev/self-host setups).
var errSMTPUnset = errors.New("smtp not configured")

// sendMail delivers a plain-text email via the configured SMTP server.
func sendMail(cfg *config.Config, to, subject, body string) error {
	smtpCfg := cfg.Auth.SMTP
	if smtpCfg.Host == "" {
		return errSMTPUnset
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", smtpCfg.From, to, subject, body)
	addr := smtpCfg.Host + ":" + smtpCfg.Port
	var authMech smtp.Auth
	if smtpCfg.User != "" {
		authMech = smtp.PlainAuth("", smtpCfg.User, smtpCfg.Pass, smtpCfg.Host)
	}
	return smtp.SendMail(addr, authMech, smtpCfg.From, []string{to}, []byte(msg))
}
