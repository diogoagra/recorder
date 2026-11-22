# Segurança

## Relatar uma falha

Se você encontrou uma falha de segurança no Recorder, **não abra uma issue
pública**. Use o relato privado do GitHub: na página do repositório, aba
**Security → Report a vulnerability**.

Ajuda muito dizer:

- a versão do app (está em "Sobre o Recorder") ou o commit, e o sistema
  (macOS ou Windows);
- o que um atacante precisa ter: falar numa reunião gravada, mandar um áudio
  para ser importado, rodar um programa no mesmo computador, abrir uma página
  no navegador da vítima…;
- qual IA estava escolhida (Ollama, agy ou Claude), se isso importa;
- os passos para reproduzir, ou um teste que falha.

Depois da correção, o relato é publicado com o crédito a quem o encontrou, se
a pessoa quiser.

## O que o app protege

O que fica no computador e o que sai dele, de quem o app se defende, como, e
os limites conhecidos estão em [docs/seguranca.md](docs/seguranca.md).

## Versões

As correções entram na versão mais recente (1.0.x).

---

*Security issues: please report privately through GitHub (Security → Report a
vulnerability) rather than opening a public issue. The threat model is
described, in Portuguese, in [docs/seguranca.md](docs/seguranca.md).*
