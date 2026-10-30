const menu = document.querySelector('.docs-menu');
const sidebar = document.querySelector('.sidebar');
menu?.addEventListener('click', () => {
  const open = sidebar.classList.toggle('is-open');
  menu.setAttribute('aria-expanded', String(open));
});
const search = document.querySelector('.doc-search');
search?.addEventListener('input', () => {
  const query = search.value.trim().toLocaleLowerCase('ru');
  let count = 0;
  for (const link of sidebar.querySelectorAll('a[data-search]')) {
    const match = link.dataset.search.toLocaleLowerCase('ru').includes(query);
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
  button.textContent = 'Копировать';
  button.setAttribute('aria-label', 'Копировать пример кода');
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(pre.querySelector('code').textContent);
      button.textContent = 'Скопировано';
      setTimeout(() => { button.textContent = 'Копировать'; }, 1800);
    } catch {
      button.textContent = 'Выделите код';
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(pre.querySelector('code'));
      selection.removeAllRanges(); selection.addRange(range);
    }
  });
  pre.append(button);
}
