<#ftl output_format="plainText">
Olá<#if user.firstName??> ${user.firstName}</#if>,

Uma conta no ${realmName} foi criada com este endereço de email. Confirme clicando no link abaixo:

${link}

Este link expira em ${linkExpirationFormatter(linkExpiration)}.

Se você não criou esta conta, ignore este email.

— Equipe ${realmName}
