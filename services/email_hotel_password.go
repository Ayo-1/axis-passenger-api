package services

import (
	"fmt"
	"log"
	"os"

	"github.com/resend/resend-go/v2"
)

// HotelPasswordLinkData feeds the set/reset password email.
type HotelPasswordLinkData struct {
	EmailHeader
	HotelName   string
	Email       string
	Intro       string
	ButtonLabel string
	Link        string
	ExpiresIn   string // "1 hour" / "7 days"
	IsInvite    bool
}

// hotelPasswordLinkBody is the {{template "body" .}} block for the shared layout.
const hotelPasswordLinkBody = `
{{define "extra_style"}}
  .btn-wrap { text-align:center; margin:24px 0 20px 0; }
  .btn { display:inline-block; background:#c8ea3e; color:#10161b !important; text-decoration:none; font-weight:700; font-size:14px; padding:14px 28px; border-radius:10px; }
  .muted { font-size:13px; color:#6f6f6f; line-height:1.6; margin:0 0 12px 0; }
  .link { word-break:break-all; font-size:12px; color:#3d3d3d; }
  .steps { margin:0; padding:0 0 0 18px; font-size:14px; color:#3d3d3d; line-height:1.7; }
{{end}}
{{define "body"}}
  <p style="font-size:15px;color:#1a1a1a;line-height:1.6;margin:0 0 16px 0;">
    Hi {{.HotelName}} team,
  </p>
  <p style="font-size:15px;color:#3d3d3d;line-height:1.6;margin:0 0 8px 0;">{{.Intro}}</p>

  <div class="card">
    <p class="label">Partner account</p>
    <p class="value">{{.Email}}</p>
    <div class="divider"></div>
    <p class="label">Link valid for</p>
    <p class="value" style="margin-bottom:0;">{{.ExpiresIn}}</p>
  </div>

  <div class="btn-wrap">
    <a class="btn" href="{{.Link}}">{{.ButtonLabel}}</a>
  </div>

  {{if .IsInvite}}
  <p class="muted">Once your password is set you can:</p>
  <ol class="steps">
    <li>Add your property and payout details.</li>
    <li>Book airport transfers and rentals for guests.</li>
    <li>Track commission and request payouts.</li>
  </ol>
  {{end}}

  <div class="notice">
    <p class="notice-title">Didn't request this?</p>
    <p class="notice-body">
      {{if .IsInvite}}If you weren't expecting this invitation, you can ignore this email and no account will be activated.{{else}}If you didn't ask to reset your password, ignore this email. Your current password still works.{{end}}
    </p>
  </div>

  <p class="muted" style="margin-top:16px;">If the button doesn't work, paste this link into your browser:</p>
  <p class="link">{{.Link}}</p>
{{end}}
`

// RenderHotelPasswordLinkEmail returns subject and HTML for an invite
// (first-time password after approval) or a reset.
func RenderHotelPasswordLinkEmail(hotelName, email, link, purpose string) (subject, html string, err error) {
	d := HotelPasswordLinkData{
		EmailHeader: EmailHeader{Brand: DefaultBrand()},
		HotelName:   hotelName,
		Email:       email,
		Link:        link,
	}
	if purpose == "invite" {
		d.IsInvite = true
		d.Eyebrow = "You're approved"
		d.HeaderTitle = "Welcome to the Axis partner network"
		d.Subtitle = "Set a password to open your dashboard."
		d.Intro = fmt.Sprintf("%s has been approved as an Axis hotel partner. Choose a password to finish setting up your account.", hotelName)
		d.ButtonLabel = "Set my password"
		d.ExpiresIn = "7 days"
		subject = fmt.Sprintf("%s is approved on Axis — set your password", hotelName)
	} else {
		d.Eyebrow = "Password reset"
		d.HeaderTitle = "Reset your partner password"
		d.Subtitle = "This link works once and expires soon."
		d.Intro = "We received a request to reset the password for your Axis partner account. Use the button below to choose a new one."
		d.ButtonLabel = "Choose a new password"
		d.ExpiresIn = "1 hour"
		subject = "Reset your Axis partner password"
	}
	html, err = renderEmail(hotelPasswordLinkBody, d)
	return subject, html, err
}

// SendHotelPasswordLinkEmail emails a set-password (invite) or reset link.
func (e *EmailService) SendHotelPasswordLinkEmail(to, hotelName, link, purpose string) error {
	if e.client == nil {
		log.Printf("Would send hotel %s password link to %s (email disabled): %s", purpose, to, link)
		return nil
	}

	subject, htmlContent, err := RenderHotelPasswordLinkEmail(hotelName, to, link, purpose)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: subject,
		Html:    htmlContent,
	}

	if _, err := e.client.Emails.Send(params); err != nil {
		log.Printf("Failed to send hotel %s password link to %s: %v", purpose, to, err)
		return err
	}

	log.Printf("Hotel %s password link sent to %s", purpose, to)
	return nil
}
