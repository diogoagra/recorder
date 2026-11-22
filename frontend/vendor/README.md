# Bibliotecas e fonte embutidas

O Recorder não carrega nada de CDN: o que a tela usa de terceiros fica aqui e
vai dentro do app (o `main.go` embute a pasta `frontend/` inteira). Assim o app
funciona sem internet e a versão não muda sozinha.

Os arquivos são cópias exatas dos publicados (não foram editados). Para
conferir: `shasum -a 256 <arquivo>`. Para atualizar, baixe a versão nova da
mesma origem, troque o arquivo e atualize esta tabela.

| Arquivo | O que é | Versão | Origem | SHA-256 |
|---|---|---|---|---|
| `marked.min.js` | Markdown do resumo e do chat para HTML | marked 15.0.12 (MIT) | https://cdn.jsdelivr.net/npm/marked@15.0.12/marked.min.js | `3e7e7d7feb3e5d58cb6c804f68ab5c24cc7e5eb6270fd6e5cbb9124739217d0c` |
| `purify.min.js` | Limpa o HTML gerado pelo marked antes de ir para a tela | DOMPurify 3.4.17 (Apache-2.0 ou MPL-2.0) | https://cdn.jsdelivr.net/npm/dompurify@3.4.17/dist/purify.min.js | `cd37898f9782adf54820c413191b5691803f0f137c799d6edcfd38523b4c2c31` |
| `fonts/inter-latin.woff2` | Fonte Inter, subconjunto latin, variável (pesos 400 a 600 em uso) | Inter v20 do Google Fonts (OFL-1.1) | https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa1ZL7W0Q5nw.woff2 | `c940764593d0fe5d596be327ca7558855e018039fb78509aa21921fd3644c3e4` |
| `fonts/inter-latin-ext.woff2` | Fonte Inter, subconjunto latin-ext, variável | Inter v20 do Google Fonts (OFL-1.1) | https://fonts.gstatic.com/s/inter/v20/UcC73FwrK3iLTeHuS_nVMrMxCp50SjIa25L7W0Q5n-wU.woff2 | `a28eb6d3ccb534ae0c94ca999371df024aab60b08c3c8a5720ee9e32fa0faaa2` |
| `fonts/OFL.txt` | Licença da Inter, que acompanha a fonte | SIL Open Font License 1.1 | https://raw.githubusercontent.com/google/fonts/main/ofl/inter/OFL.txt | `5b9321a4298cfeb6b34354164a1c3afc3db114569984c502b9b35d988fd58c57` |
| `marked-LICENSE.md` | Licença do marked, que acompanha a biblioteca | MIT | https://raw.githubusercontent.com/markedjs/marked/v15.0.12/LICENSE.md | `8e3a3f82f59a60958f56ca08f445647c32a4733dc7ca6c2c46f6eb898471ab9c` |
| `purify-LICENSE.txt` | Licença do DOMPurify, que acompanha a biblioteca | Apache 2.0 e MPL 2.0 | https://raw.githubusercontent.com/cure53/DOMPurify/3.4.17/LICENSE | `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30` |

Os endereços das fontes vieram de
`https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&display=swap`
(o mesmo que o `index.html` carregava), em 12/11/2026. Os `@font-face` ficam no
começo do `style.css`. Só os subconjuntos latin e latin-ext foram embutidos:
texto em cirílico, grego ou vietnamita usa a fonte do sistema.

O `app.js` usa as duas bibliotecas num lugar só, `renderMarkdown`. Sem elas, o
texto da IA aparece como veio, sem formatação.
