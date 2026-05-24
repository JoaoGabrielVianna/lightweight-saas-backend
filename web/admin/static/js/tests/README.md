# Frontend tests

Pequena suíte vanilla — sem package manager, sem framework. Roda no Node
18+ usando os módulos built-in `node:test` e `node:assert/strict`.

## Como rodar

```sh
node --test web/admin/static/js/tests/
```

Saída esperada (resumida):

```
✔ web/admin/static/js/tests/state.test.mjs ...
✔ web/admin/static/js/tests/locale.test.mjs ...
ℹ tests 13
ℹ pass  13
ℹ fail  0
```

## O que cada arquivo cobre

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

## Stub de `localStorage`

Os módulos sob teste usam `localStorage`. Cada arquivo de teste cria um
stub no topo (antes do primeiro `import` dinâmico de `state.js`/`locale.js`),
porque `state.js` lê `localStorage` no carregamento do módulo para semear
`_state.locale`.

## Por que não há teste de `docsView`

Cobertura de `docsView` exigiria mockar `document`, `fetch`, `mount`,
`renderMermaidBlocks`, etc. O ganho marginal não compensa a fragilidade
do mock. A regressão do loop é testada **a montante** — em `state.js` e
`locale.js`, onde o mecanismo realmente vive. Se essas duas camadas se
comportam corretamente, `docsView` não pode entrar em loop.

Para validação end-to-end do freeze, ver o relatório
[`docs/archive/DOCS_PERFORMANCE_REPORT.md`](../../../../docs/archive/DOCS_PERFORMANCE_REPORT.md)
e a contagem de requests em `docker logs saas-api`.
