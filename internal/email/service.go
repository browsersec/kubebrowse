package email

import (
	"bytes"
	"fmt"
	"html/template"
	"net/smtp"
	"os"
	"strconv"

	"github.com/sirupsen/logrus"
)

type Service struct {
	smtpHost     string
	smtpPort     int
	smtpUsername string
	smtpPassword string
	fromEmail    string
	fromName     string
	baseURL      string
}

type EmailData struct {
	To           string
	Subject      string
	TemplateName string
	Data         interface{}
}

type VerificationEmailData struct {
	Name            string
	Email           string
	VerificationURL string
	BaseURL         string
}

func NewService() *Service {
	smtpPort, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if smtpPort == 0 {
		smtpPort = 587 // Default SMTP port
	}

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "https://localhost:4000"
	}

	return &Service{
		smtpHost:     os.Getenv("SMTP_HOST"),
		smtpPort:     smtpPort,
		smtpUsername: os.Getenv("SMTP_USERNAME"),
		smtpPassword: os.Getenv("SMTP_PASSWORD"),
		fromEmail:    os.Getenv("FROM_EMAIL"),
		fromName:     os.Getenv("FROM_NAME"),
		baseURL:      baseURL,
	}
}

func (s *Service) IsConfigured() bool {
	return s.smtpHost != "" && s.smtpUsername != "" && s.smtpPassword != "" && s.fromEmail != ""
}

func (s *Service) SendVerificationEmail(to, name, token string) error {
	if !s.IsConfigured() {
		logrus.Warn("Email service not configured - skipping email verification")
		return fmt.Errorf("email service not configured")
	}

	verificationURL := fmt.Sprintf("%s/auth/verify-email?token=%s", s.baseURL, token)

	data := VerificationEmailData{
		Name:            name,
		Email:           to,
		VerificationURL: verificationURL,
		BaseURL:         s.baseURL,
	}

	subject := "Verify your email address"
	htmlBody, err := s.renderTemplate("verification", data)
	if err != nil {
		return fmt.Errorf("failed to render email template: %w", err)
	}

	return s.sendEmail(to, subject, htmlBody)
}

func (s *Service) sendEmail(to, subject, htmlBody string) error {
	auth := smtp.PlainAuth("", s.smtpUsername, s.smtpPassword, s.smtpHost)

	fromName := s.fromName
	if fromName == "" {
		fromName = "KubeBrowse"
	}

	msg := fmt.Sprintf("From: %s <%s>\r\n", fromName, s.fromEmail)
	msg += fmt.Sprintf("To: %s\r\n", to)
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += "MIME-Version: 1.0\r\n"
	msg += "Content-Type: text/html; charset=UTF-8\r\n"
	msg += "\r\n"
	msg += htmlBody

	addr := fmt.Sprintf("%s:%d", s.smtpHost, s.smtpPort)
	err := smtp.SendMail(addr, auth, s.fromEmail, []string{to}, []byte(msg))
	if err != nil {
		logrus.Errorf("Failed to send email to %s: %v", to, err)
		return fmt.Errorf("failed to send email: %w", err)
	}

	logrus.Infof("Verification email sent successfully to %s", to)
	return nil
}

func (s *Service) renderTemplate(templateName string, data interface{}) (string, error) {
	var tmplContent string

	switch templateName {
	case "verification":
		tmplContent = verificationEmailTemplate
	default:
		return "", fmt.Errorf("unknown template: %s", templateName)
	}

	tmpl, err := template.New(templateName).Parse(tmplContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

const verificationEmailTemplate = `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Verify Your Email</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 600px;
            margin: 0 auto;
            padding: 20px;
        }
        .header {
            background-color: #4f46e5;
            color: white;
            padding: 20px;
            text-align: center;
            border-radius: 8px 8px 0 0;
        }
        .content {
            background-color: #f9fafb;
            padding: 30px;
            border-radius: 0 0 8px 8px;
        }
        .button {
            display: inline-block;
            background-color: #4f46e5;
            color: white;
            padding: 12px 24px;
            text-decoration: none;
            border-radius: 6px;
            margin: 20px 0;
        }
        .footer {
            margin-top: 30px;
            padding-top: 20px;
            border-top: 1px solid #e5e7eb;
            font-size: 14px;
            color: #6b7280;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>Welcome to KubeBrowse!</h1>
    </div>
    <div class="content">
        <h2>Verify Your Email Address</h2>
        <p>Hi{{if .Name}} {{.Name}}{{end}},</p>
        <p>Thank you for signing up for KubeBrowse! To complete your registration and start using our browser sandbox service, please verify your email address by clicking the button below:</p>
        
        <div style="text-align: center;">
            <a href="{{.VerificationURL}}" class="button">Verify Email Address</a>
        </div>
        
        <p>If the button doesn't work, you can also copy and paste this link into your browser:</p>
        <p style="word-break: break-all; background-color: #f3f4f6; padding: 10px; border-radius: 4px;">
            {{.VerificationURL}}
        </p>
        
        <p><strong>This verification link will expire in 24 hours.</strong></p>
        
        <p>If you didn't create an account with KubeBrowse, you can safely ignore this email.</p>
    </div>
    <div class="footer">
        <p>Best regards,<br>The KubeBrowse Team</p>
        <p>This email was sent to {{.Email}}. If you have any questions, please contact our support team.</p>
    </div>
</body>
</html>
`
