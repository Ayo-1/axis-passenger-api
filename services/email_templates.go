// services/email_templates.go
package services

import (
	"bytes"
	"html/template"
)


type EmailHeader struct {
	Brand
	Eyebrow     string
	HeaderTitle string
	Subtitle    string
}
// Shared chrome. {{template "body" .}} is the only thing each email type supplies.
const emailLayout = `
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="light">
<meta name="supported-color-schemes" content="light">
<style>
  :root { color-scheme: light only; supported-color-schemes: light; }
  body,table,td { margin:0; padding:0; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif; }
  body { background:#f2f2f2; color:#1a1a1a; }
  .wrap { max-width:560px; margin:0 auto; background:#ffffff; }
  .header{background-color:#10161b;background-repeat:no-repeat;background-position:right bottom;background-size:165px auto}
  .header-content{padding:32px 32px 70px}
  .logo{height:32px;display:block;margin-bottom:28px}
  .eyebrow,.title{font-weight:700;margin:0 0 8px}
  .eyebrow{color:#c8ea3e;font-size:12px;letter-spacing:.5px;text-transform:uppercase}
  .title{color:#fff;font-size:22px;line-height:1.25}
  .subtitle{color:#a9a9a9;font-size:14px;line-height:1.5;margin:0}
  .body { padding:28px 32px 8px 32px; }
  .card { background:#f6f7f6; border-radius:12px; padding:20px; }
  .label { font-size:11px; letter-spacing:.5px; text-transform:uppercase; color:#8a8a8a; font-weight:600; margin:0 0 4px 0; }
  .value { font-size:15px; font-weight:600; color:#111; margin:0 0 14px 0; }
  .divider { border-top:1px solid #e4e4e4; margin:4px 0 16px 0; }
  .badge { display:inline-block; background:#e6f6ea; color:#1f9d55; font-size:12px; font-weight:700; padding:5px 12px; border-radius:20px; }
  .notice { background:#eef4f0; border-radius:10px; padding:16px 18px; margin:20px 0 8px 0; }
  .notice-title { color:#1f9d55; font-size:12px; font-weight:700; letter-spacing:.5px; text-transform:uppercase; margin:0 0 6px 0; }
  .notice-body { font-size:13px; color:#3d3d3d; line-height:1.5; margin:0; }
  .footer { background:#0d1210; background-repeat:repeat; background-size:220px; padding:28px 32px; margin-top:16px; }
  .footer-name { color:#ffffff; font-size:14px; font-weight:700; margin:0 0 6px 0; }
  .footer-addr { color:#9aa39a; font-size:12px; line-height:1.6; margin:0; }
  .social img { width:28px; height:28px; margin-right:8px; }
  .social { margin-top:5px; }
  .copyright { color:#6f7a70; font-size:11px; margin:16px 0 0 0; }
  {{block "extra_style" .}}{{end}}
</style>
</head>
<body>
  <div class="wrap">
    <!--[if mso]>
    <v:rect xmlns:v="urn:schemas-microsoft-com:vml" fill="true" stroke="false" style="width:560px;height:330px;">
      <v:fill type="frame" color="#10161B" src="{{.HeaderIllustrationURL}}" />
      <v:textbox inset="0,0,0,0">
    <![endif]-->
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" class="header"
      style="background-color:#10161B;background-image:url('{{.HeaderIllustrationURL}}');background-repeat:no-repeat;background-position:right bottom;background-size:165px auto;">
      <tr>
        <td valign="top" class="header-content" style="padding:32px 32px 70px 32px;">
          <img class="logo" src="{{.LogoURL}}" alt="{{.CompanyName}}" height="32" style="height:32px;display:block;margin-bottom:28px;">
          <p class="eyebrow">{{.Eyebrow}}</p>
          <h1 class="title">{{.HeaderTitle}}</h1>
          <p class="subtitle">{{.Subtitle}}</p>
        </td>
      </tr>
    </table>
    <!--[if mso]></v:textbox></v:rect><![endif]-->

    <div class="body">
      {{template "body" .}}
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

// renderEmail parses the shared layout + one body template together and
// executes "layout". data must satisfy emailHeader (Brand + header fields).
func renderEmail(bodyTmpl string, data any) (string, error) {
	t, err := template.New("layout").Parse(emailLayout)
	if err != nil {
		return "", err
	}
	if _, err := t.Parse(bodyTmpl); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}