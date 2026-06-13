<#ftl output_format="plainText">
Olá<#if user.firstName??> ${user.firstName}</#if>,

Você foi convidado para acessar o ${realmName}. Acesse o link abaixo para ativar sua conta:

${link}

Este link expira em ${linkExpirationFormatter(linkExpiration)}.

Se você não esperava esse convite, ignore este email.

— Equipe ${realmName}
