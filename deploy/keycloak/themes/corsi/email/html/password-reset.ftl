<#ftl output_format="HTML">
<html>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#111;margin:0;padding:0;background:#f5f5f5;">
<table width="100%" cellpadding="0" cellspacing="0" style="background:#f5f5f5;padding:40px 0;">
  <tr><td align="center">
    <table width="600" cellpadding="0" cellspacing="0" style="background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 2px 8px rgba(0,0,0,0.08);">
      <tr><td style="background:#6366f1;padding:32px 40px;">
        <p style="margin:0;color:#fff;font-size:20px;font-weight:700;">${realmName}</p>
      </td></tr>
      <tr><td style="padding:40px;">
        <p style="margin:0 0 16px;font-size:16px;line-height:1.6;">Olá<#if user.firstName??> ${user.firstName}</#if>,</p>
        <p style="margin:0 0 24px;font-size:15px;line-height:1.6;color:#444;">
          Recebemos uma solicitação para redefinir a senha da sua conta no <strong>${realmName}</strong>.
          Clique no botão abaixo para escolher uma nova senha.
        </p>
        <p style="text-align:center;margin:32px 0;">
          <a href="${link}" style="background:#6366f1;color:#fff;padding:14px 32px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;display:inline-block;">Redefinir senha</a>
        </p>
        <p style="margin:0 0 8px;font-size:13px;color:#888;">Este link expira em ${linkExpirationFormatter(linkExpiration)}.</p>
        <p style="margin:0;font-size:13px;color:#888;">Se você não solicitou isso, pode ignorar este email — sua senha não será alterada.</p>
      </td></tr>
      <tr><td style="background:#f9f9f9;padding:20px 40px;border-top:1px solid #eee;">
        <p style="margin:0;font-size:12px;color:#aaa;text-align:center;">© ${realmName} — enviado automaticamente, não responda este email.</p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body>
</html>
