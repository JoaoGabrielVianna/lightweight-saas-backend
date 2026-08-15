# Frontend tests

Pequena suíte vanilla — sem package manager, sem framework. Roda no Node
18+ usando os módulos built-in `node:test` e `node:assert/strict`.

## Como rodar

```sh
node --test web/admin/static/js/tests/
```

Ou via `make test-frontend` (também roda dentro de `make ci-full`).

Saída esperada (resumida):

```
ℹ tests 143
ℹ pass  143
ℹ fail  0
```

## O que cada arquivo cobre

### Workspace (Slice 6)

- **api-errors.test.mjs** — os dois envelopes de erro:
  - `/admin/*` (`{"error":"..."}`) e `/v1` (`{"error":{code,message,request_id}}`);
  - corpo malformado, array, número, HTML de gateway — nada vira `[object Object]`;
  - `request_id` do corpo, com fallback para o header `X-Request-Id`;
  - **regression** — o envelope `/v1` renderizava literalmente `[object Object]`.
- **wspath.test.mjs** — construção de caminhos:
  - `wsPath` (URL da API) e `wsRoute` (rota do hash);
  - id ausente, id que não é `ws_`, URL absoluta e caminho já com `/v1` são rejeitados;
  - `swapWorkspaceInPath` mantém a página e **descarta ids de entidade**.
- **workspace-state.test.mjs** — carga, seleção e persistência:
  - precedência rota > preferência persistida > primeiro ativo > nenhum;
  - seleção persistida inválida (arquivada/inexistente) cai para outra **e** some do storage;
  - zero workspaces não faz fallback silencioso para `/admin/*`;
  - `classifyConnection` / `canWriteHere` leem `can_write`, nunca `access_mode`.
- **workspace-isolation.test.mjs** — a corrida que motiva a fatia:
  - resposta lenta de A descartada depois da troca para B;
  - a troca **aborta** as requisições em voo da workspace que sai;
  - A → B → A continua stale para o primeiro pedido de A;
  - troca força refetch da conexão; cargas concorrentes compartilham um pedido;
  - o gate bloqueia sem conexão/arquivada e **não** bloqueia `unhealthy` nem `read_only`.
- **workspace-mutations.test.mjs** — segurança de mutação:
  - a troca fecha todos os diálogos abertos;
  - `wsMutate` recusa ação composta em outra workspace e **não a redireciona**;
  - conexão read-only desabilita escrita antes da rede.
- **workspace-management.test.mjs** — superfície de gerenciamento:
  - segredo vai em um único campo; em branco na edição **omite** a chave;
  - o estado do console nunca guarda segredo;
  - estados degradados sempre oferecem um caminho; markup do provider vira texto.
- **workspace-selector.test.mjs** — o seletor no topbar:
  - workspace selecionada renderizada por nome, com o estado da conexão;
  - arquivadas não são selecionáveis; zero workspaces mostra call to action;
  - escolher uma workspace **navega** (a rota é a fonte da verdade).

### Anteriores

- **state.test.mjs** — `setState` / `subscribe` / iteração:
  - snapshot impede subscriber adicionado durante dispatch de disparar
    no mesmo ciclo;
  - reentrancy guard colapsa `setState` aninhado em um único dispatch;
  - unsubscribe dentro de callback tem efeito imediato;
  - **regression** — padrão "callback re-registra a si próprio" não
    causa recursão (era a causa raiz do freeze de 74.655 fetches).
- **locale.test.mjs** — `onLocaleChange` + seed de `_state.locale`:
  - seed lê `localStorage.admin_docs_locale` no module-load;
  - subscriber não dispara enquanto locale não muda;
  - dispara exatamente 1 vez por mudança real;
  - **regression** — PT-BR persistido + setStates não-locale → 0 disparos
    (não há mais loop com 74k fetches);
  - **regression** — `setLocale` dispara o callback exatamente uma vez,
    mesmo com o padrão re-register usado pelo docsView.

## Stubs (`helpers.mjs`)

`helpers.mjs` não é um arquivo de teste — o runner só executa `*.test.mjs`.
Ele concentra três stubs:

- **`makeStorage`** — `localStorage` / `sessionStorage`. Cada arquivo instala o
  stub no topo, antes do primeiro `import` dinâmico, porque `state.js` lê
  `localStorage` no carregamento do módulo para semear `_state.locale`.
- **`installDOM`** — ~80 linhas cobrindo exatamente o que `lib/dom.js` e
  `components/modal.js` tocam. Deliberadamente não é jsdom: seria a primeira
  dependência de runtime do repositório, num console que não tem nenhuma.
- **`fetchStub` / `deferred`** — `fetch` roteável com atraso controlável, que é
  o que permite dirigir a corrida "A sai, B chega antes, A volta depois" de
  forma determinística em vez de por temporização.

## Por que não há teste de `docsView`

Cobertura de `docsView` exigiria mockar `document`, `fetch`, `mount`,
`renderMermaidBlocks`, etc. O ganho marginal não compensa a fragilidade
do mock. A regressão do loop é testada **a montante** — em `state.js` e
`locale.js`, onde o mecanismo realmente vive. Se essas duas camadas se
comportam corretamente, `docsView` não pode entrar em loop.

Para validação end-to-end do freeze, ver o relatório
[`docs/archive/DOCS_PERFORMANCE_REPORT.md`](../../../../../docs/archive/DOCS_PERFORMANCE_REPORT.md)
e a contagem de requests em `docker logs saas-api`.
