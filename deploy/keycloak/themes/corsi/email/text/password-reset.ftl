<#ftl output_format="plainText">
Olá<#if user.firstName??> ${user.firstName}</#if>,

Recebemos uma solicitação para redefinir a senha da sua conta no ${realmName}. Acesse o link abaixo:

${link}

Este link expira em ${linkExpirationFormatter(linkExpiration)}.

Se você não solicitou isso, ignore este email.

— Equipe ${realmName}
