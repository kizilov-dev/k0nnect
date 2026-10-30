const language = document.documentElement.lang === 'ru' ? 'ru' : 'en';
const labels = language === 'ru'
  ? {copy: 'Копировать', copied: 'Скопировано', copyLabel: 'Копировать пример кода', status: 'Код скопирован', select: 'Выделить код'}
  : {copy: 'Copy', copied: 'Copied', copyLabel: 'Copy code example', status: 'Code copied to clipboard', select: 'Select code'};
document.querySelector('[data-language-switch]')?.addEventListener('change', event => {
  window.location.assign(event.target.value);
});
const navToggle = document.querySelector('.menu-toggle');
const siteNav = document.querySelector('.nav');
function closeNav() {
  siteNav?.classList.remove('is-open');
  navToggle?.setAttribute('aria-expanded', 'false');
}
navToggle?.addEventListener('click', () => {
  const open = siteNav.classList.toggle('is-open');
  navToggle.setAttribute('aria-expanded', String(open));
});
siteNav?.addEventListener('click', event => {
  if (event.target.closest('a')) closeNav();
});
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && navToggle?.getAttribute('aria-expanded') === 'true') {
    closeNav(); navToggle.focus();
  }
});
const menu = document.querySelector('.docs-menu');
const sidebar = document.querySelector('.sidebar');
menu?.addEventListener('click', () => {
  const open = sidebar.classList.toggle('is-open');
  menu.setAttribute('aria-expanded', String(open));
});
const search = document.querySelector('.doc-search');
search?.addEventListener('input', () => {
  const query = search.value.trim().toLocaleLowerCase(language);
  let count = 0;
  for (const link of sidebar.querySelectorAll('a[data-search]')) {
    const match = link.dataset.search.toLocaleLowerCase(language).includes(query);
    link.hidden = !match;
    if (match) count++;
  }
  for (const group of sidebar.querySelectorAll('.nav-group')) {
    group.hidden = ![...group.querySelectorAll('a')].some(a => !a.hidden);
  }
  document.querySelector('.search-empty').hidden = count > 0;
});
for (const pre of document.querySelectorAll('pre[data-copy]')) {
  const button = document.createElement('button');
  button.className = 'copy';
  button.textContent = labels.copy;
  button.setAttribute('aria-label', labels.copyLabel);
  const status = document.createElement('span');
  status.className = 'sr-only';
  status.setAttribute('role', 'status');
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(pre.querySelector('code').textContent);
      button.textContent = labels.copied; status.textContent = labels.status;
      setTimeout(() => { button.textContent = labels.copy; status.textContent = ''; }, 1800);
    } catch {
      button.textContent = labels.select;
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(pre.querySelector('code'));
      selection.removeAllRanges(); selection.addRange(range);
    }
  });
  pre.append(button, status);
}
