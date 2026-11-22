// Tema antes de desenhar a tela: o escolhido no app ou, sem escolha, o do
// sistema. Fica sempre em data-theme, que o style.css usa. É carregado no
// <head>, antes do style.css, para a tela não piscar com o tema errado.
{
  let theme = null;
  try { theme = localStorage.getItem("theme"); } catch (e) {}
  if (theme !== 'dark' && theme !== 'light') {
    theme = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="color-scheme"]').content = theme;
}
