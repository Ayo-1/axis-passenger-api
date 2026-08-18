// services/email.go
package services

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"os"

	"github.com/resend/resend-go/v2"
)

type EmailService struct {
	client *resend.Client
}

// NOTE: OTPData and BookingConfirmationData are NOT declared here anymore.
// They live in brand.go (with Brand embedded). Do not redeclare them in
// this file or you'll get a "OTPData redeclared" / "BookingConfirmationData
// redeclared" compile error.

func NewEmailService() *EmailService {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Println("Warning: RESEND_API_KEY not set")
		return &EmailService{client: nil}
	}

	client := resend.NewClient(apiKey)
	return &EmailService{client: client}
}

// Send OTP Email
func (e *EmailService) SendOTPEmail(to, otp string) error {
	if e.client == nil {
		log.Printf("Would send OTP %s to %s (email disabled)", otp, to)
		return nil
	}

	data := OTPData{
		Brand:         DefaultBrand(),
		Title:         "Verify Your Email Address",
		Body:          "Use the code below to complete your verification.",
		OTP:           otp,
		ExpiryMinutes: 10,
	}

	htmlContent, err := e.renderOTPTemplate(data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: fmt.Sprintf("Your OTP for %s", data.AppName),
		Html:    htmlContent,
	}

	_, err = e.client.Emails.Send(params)
	if err != nil {
		log.Printf("Failed to send OTP email: %v", err)
		return err
	}

	log.Printf("OTP email sent to %s", to)
	return nil
}

// Send Booking Confirmation
// mapURL: build this with buildMapURL(pickupLat, pickupLng, dropLat, dropLng)
// before calling this function — it needs real coordinates, not a brand asset.
func (e *EmailService) SendBookingConfirmation(to, bookingID, pickup, dropoff, date, fareRange, mapURL string) error {
	if e.client == nil {
		log.Printf("Would send booking confirmation to %s (email disabled)", to)
		return nil
	}

	data := BookingConfirmationData{
		Brand:          DefaultBrand(),
		BookingID:      bookingID,
		Status:         "CONFIRMED",
		PickupAddress:  pickup,
		DropoffAddress: dropoff,
		DateTime:       date,
		FareRange:      fareRange,
		MapImageURL:    mapURL,
		NoticeText:     "Your driver will meet you at the pickup location. Keep your app open to track them as they approach.",
	}

	htmlContent, err := e.renderBookingTemplate(data)
	if err != nil {
		return err
	}

	params := &resend.SendEmailRequest{
		From:    os.Getenv("EMAIL_FROM"),
		To:      []string{to},
		Subject: fmt.Sprintf("Booking Confirmation #%s", bookingID),
		Html:    htmlContent,
	}

	_, err = e.client.Emails.Send(params)
	return err
}

// Render OTP HTML Template
func (e *EmailService) renderOTPTemplate(data OTPData) (string, error) {
	tmpl := `
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="light">
<meta name="supported-color-schemes" content="light">
<style>
  :root {
    color-scheme: light only;
    supported-color-schemes: light;
  }
  body,table,td { margin:0; padding:0; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif; }
  body { background:#f2f2f2; color:#1a1a1a; }
  .wrap { max-width:560px; margin:0 auto; background:#ffffff; }

 
  .header{background-color:#10161b;background-repeat:no-repeat;background-position:right bottom;background-size:165px auto}
  .header-content{padding:32px 32px 70px}
  .logo{height:32px;display:block;margin-bottom:28px}
  .eyebrow,.title{font-weight:700;margin:0 0 8px}
  .eyebrow{color:#c8ea3e;font-size:12px;letter-spacing:.5px;text-transform:uppercase}
  .title{color:#fff;font-size:20px;line-height:1.2}
  .subtitle{color:#a9a9a9;font-size:14px;line-height:1.5;margin:0}

  .body { padding:28px 32px 8px 32px; }
  .card { background:#f6f7f6; border-radius:12px; padding:28px; text-align:center; }
  .card-label { font-size:12px; letter-spacing:.5px; text-transform:uppercase; color:#8a8a8a; font-weight:700; margin:0 0 14px 0; }
  .otp-box { display:inline-block; background:#ffffff; border-radius:10px; padding:16px 28px; box-shadow:0 2px 6px rgba(0,0,0,0.06); }
  .otp-code { font-size:36px; font-weight:800; letter-spacing:10px; color:#111; }
  .expiry { font-size:13px; color:#5f5f5f; margin:16px 0 0 0; }
  .expiry strong { color:#1f9d55; }
  .notice { background:#fff8e6; border-radius:10px; padding:14px 16px; margin:20px 0 8px 0; }
  .notice p { font-size:12px; color:#8a6d1f; margin:0; line-height:1.5; }
  .footer { background:#0d1210; background-repeat:repeat; background-size:220px; padding:28px 32px; margin-top:16px; }
  .footer-name { color:#ffffff; font-size:14px; font-weight:700; margin:0 0 6px 0; }
  .footer-addr { color:#9aa39a; font-size:12px; line-height:1.6; margin:0; }
  .social img { width:28px; height:28px; margin-right:8px; }
  .social { margin-top: 5px;}
  .copyright { color:#6f7a70; font-size:11px; margin:16px 0 0 0; }
</style>
</head>
<body>
  <div class="wrap">
  <!--[if mso]>
    <v:rect
      xmlns:v="urn:schemas-microsoft-com:vml"
      fill="true"
      stroke="false"
      style="width:560px;height:330px;"
    >
      <v:fill
        type="frame"
        color="#10161B"
        src="{{.HeaderIllustrationURL}}"
      />
      <v:textbox inset="0,0,0,0">
    <![endif]-->

    <table
      role="presentation"
      width="100%"
      cellpadding="0"
      cellspacing="0"
      border="0"
      class="header"
      style="
        background-color:#10161B;
        background-image:url('{{.HeaderIllustrationURL}}');
        background-repeat:no-repeat;
        background-position:right bottom;
        background-size:165px auto;
      "
    >
      <tr>
        <td
          valign="top"
          class="header-content"
          style="padding:32px 32px 70px 32px;"
        >

          <img
            class="logo"
            src="{{.LogoURL}}"
            alt="{{.CompanyName}}"
            height="32"
            style="height:32px;display:block;margin-bottom:28px;"
          >

          <p class="eyebrow">
            Login to your dashboard
          </p>

          <h1 class="title">
            Use the code below to sign in
          </h1>

          <p class="subtitle" style="display: none;">
            Your ride has been confirmed. Here are your booking details:
          </p>

        </td>
      </tr>
    </table>

    <!--[if mso]>
      </v:textbox>
    </v:rect>
    <![endif]-->
    <div class="body">
      <div class="card">
        <p class="card-label">Your Verification Code</p>
        <div class="otp-box"><span class="otp-code">{{.OTP}}</span></div>
        <p class="expiry">Expires in <strong>{{.ExpiryMinutes}} minutes</strong></p>
      </div>
      <div class="notice">
        <p>If you didn't request this code, you can safely ignore this email — your account is still secure.</p>
      </div>
    </div>
    <div class="footer" style="background-image:url('{{.FooterPatternURL}}');">
      <p class="footer-name">{{.CompanyName}}</p>
      <p class="footer-addr">{{.CompanyAddressLine1}}<br>{{.CompanyAddressLine2}}</p>
      <div class="social">
        <a href="{{.FacebookURL}}"><img src="{{.FacebookIconURL}}" alt="Facebook"></a>
        <a href="{{.InstagramURL}}"><img src="{{.InstagramIconURL}}" alt="Instagram"></a>
        <a href="{{.LinkedInURL}}"><img src="{{.LinkedInIconURL}}" alt="LinkedIn"></a>
      </div>
      <p class="copyright">&copy; {{.Year}} {{.CompanyName}}. All rights reserved.</p>
    </div>
  </div>
</body>
</html>
`
	t, err := template.New("otp").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Render Booking Confirmation Template
func (e *EmailService) renderBookingTemplate(data BookingConfirmationData) (string, error) {
	tmpl := `
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="light">
<meta name="supported-color-schemes" content="light">
<style>
 :root {
      color-scheme: light only;
    }
  body,table,td { margin:0; padding:0; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif; }
  body { background:#f2f2f2; color:#1a1a1a; }
  .wrap { max-width:560px; margin:0 auto; background:#ffffff; }

 
  .header{background-color:#10161b;background-repeat:no-repeat;background-position:right bottom;background-size:165px auto}
  .header-content{padding:32px 32px 70px}
  .logo{height:32px;display:block;margin-bottom:28px}
  .eyebrow,.title{font-weight:700;margin:0 0 8px}
  .eyebrow{color:#c8ea3e;font-size:12px;letter-spacing:.5px;text-transform:uppercase}
  .title{color:#fff;font-size:26px;line-height:1.2}
  .subtitle{color:#a9a9a9;font-size:14px;line-height:1.5;margin:0}

  .body { padding:24px 32px 8px 32px; }
  .card { background:#f6f7f6; border-radius:12px; padding:20px; }
  .label { font-size:11px; letter-spacing:.5px; text-transform:uppercase; color:#8a8a8a; font-weight:600; margin:0 0 4px 0; }
  .code { font-size:20px; font-weight:700; color:#111; margin:0; }
  .badge { display:inline-block; background:#e6f6ea; color:#1f9d55; font-size:12px; font-weight:700; padding:5px 12px; border-radius:20px; }
  .divider { border-top:1px solid #e4e4e4; margin:16px 0; }
  .box { background:#e9f2e4; border-radius:10px; padding:14px 16px; }
  .box .label { color:#4b6b3f; }
  .box .value { font-size:13px; font-weight:700; color:#14532d; margin:0; }
  .map { width:100%; border-radius:10px; display:block; margin:16px 0; }
  .stop { padding-left:0px; position:relative; margin-bottom:14px; }
  .dot { width:8px; height:8px; border-radius:50%; position:absolute; left:0; top:5px; }
  .dot-pickup { background:#111; }
  .dot-drop { background:#8fd13f; }
  .stop-time { font-size:11px; color:#8a8a8a; text-transform:uppercase; margin:0 0 2px 0; }
  .stop-addr { font-size:14px; font-weight:600; color:#1a1a1a; margin:0; }
  .notice { background:#eef4f0; border-radius:10px; padding:16px 18px; margin:20px 0 8px 0; }
  .notice-title { color:#1f9d55; font-size:12px; font-weight:700; letter-spacing:.5px; text-transform:uppercase; margin:0 0 6px 0; }
  .notice-body { font-size:13px; color:#3d3d3d; line-height:1.5; margin:0; }
  .footer { background:#03282A; background-repeat:repeat; background-size:220px; padding:28px 32px; margin-top:16px; }
  .footer-name { color:#ffffff; font-size:14px; font-weight:700; margin:0 0 6px 0; }
  .footer-addr { color:#9aa39a; font-size:12px; line-height:1.6; margin:0; }
  .social img { width:28px; height:28px; margin-right:8px; }
  .social { margin-top: 5px;}
  .copyright { color:#6f7a70; font-size:11px; margin:16px 0 0 0; }
</style>
</head>
<body>
  <div class="wrap">
   <!--[if mso]>
    <v:rect
      xmlns:v="urn:schemas-microsoft-com:vml"
      fill="true"
      stroke="false"
      style="width:560px;height:330px;"
    >
      <v:fill
        type="frame"
        color="#10161B"
        src="{{.HeaderIllustrationURL}}"
      />
      <v:textbox inset="0,0,0,0">
    <![endif]-->

    <table
      role="presentation"
      width="100%"
      cellpadding="0"
      cellspacing="0"
      border="0"
      class="header"
      style="
        background-color:#10161B;
        background-image:url('{{.HeaderIllustrationURL}}');
        background-repeat:no-repeat;
        background-position:right bottom;
        background-size:165px auto;
      "
    >
      <tr>
        <td
          valign="top"
          class="header-content"
          style="padding:32px 32px 70px 32px;"
        >

          <img
            class="logo"
            src="{{.LogoURL}}"
            alt="{{.CompanyName}}"
            height="32"
            style="height:32px;display:block;margin-bottom:28px;"
          >

          <p class="eyebrow">
            Booking Confirmed!
          </p>

          <h1 class="title">
            Your ride is secured.
          </h1>

          <p class="subtitle">
            Your ride has been confirmed. Here are <br /> your booking details:
          </p>

        </td>
      </tr>
    </table>

    <!--[if mso]>
      </v:textbox>
    </v:rect>
    <![endif]-->
    <div class="body">
      <div class="card">
        <table width="100%" cellpadding="0" cellspacing="0">
          <tr>
            <td>
              <p class="label">Booking Code</p>
              <p class="code">ID #{{.BookingID}}</p>
            </td>
            <td align="right"><span class="badge">&#9679; {{.Status}}</span></td>
          </tr>
        </table>
        <div class="divider"></div>
        <table width="100%" cellpadding="0" cellspacing="0">
          <tr>
            <td width="48%">
              <div class="box">
                <p class="label">Estimated Fare</p>
                <p class="value">{{.FareRange}}</p>
              </div>
            </td>
            <td width="4%"></td>
            <td width="48%">
              <div class="box">
                <p class="label">Date &amp; Time</p>
                <p class="value">{{.DateTime}}</p>
              </div>
            </td>
          </tr>
        </table>

        <img class="map" src="{{.MapImageURL}}" alt="Route map">

        <div class="stop">
          <span class="dot dot-pickup"></span>
          <p class="stop-time">Pickup</p>
          <p class="stop-addr">{{.PickupAddress}}</p>
        </div>
        <div class="stop" style="margin-bottom:0;">
          <span class="dot dot-drop"></span>
          <p class="stop-time">Destination</p>
          <p class="stop-addr">{{.DropoffAddress}}</p>
        </div>
      </div>

      <div class="notice">
        <p class="notice-title">Important Notice</p>
        <p class="notice-body">{{.NoticeText}}</p>
      </div>
    </div>
    <div class="footer" style="background-image:url('{{.FooterPatternURL}}');">
      <p class="footer-name">{{.CompanyName}}</p>
      <p class="footer-addr">{{.CompanyAddressLine1}}<br>{{.CompanyAddressLine2}}</p>
      <div class="social">
        <a href="{{.FacebookURL}}"><img src="{{.FacebookIconURL}}" alt="Facebook"></a>
        <a href="{{.InstagramURL}}"><img src="{{.InstagramIconURL}}" alt="Instagram"></a>
        <a href="{{.LinkedInURL}}"><img src="{{.LinkedInIconURL}}" alt="LinkedIn"></a>
      </div>
      <p class="copyright">&copy; {{.Year}} {{.CompanyName}}. All rights reserved.</p>
    </div>
  </div>
</body>
</html>
`
	t, err := template.New("booking").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}