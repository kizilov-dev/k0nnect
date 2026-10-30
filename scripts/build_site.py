#!/usr/bin/env python3
"""Build the dependency-free static documentation site for GitHub Pages."""
import argparse
import html
import re
import shutil
import unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PAGES = [
    ('index', 'Обзор', 'Начать'),
    ('getting-started', 'Быстрый старт', 'Начать'),
    ('configuration', 'Конфигурация', 'Начать'),
    ('compatibility', 'Совместимость', 'Устройство'),
    ('architecture', 'Архитектура', 'Устройство'),
    ('security', 'Права и секреты', 'Устройство'),
    ('operations', 'Эксплуатация', 'Работа с утилитой'),
    ('verification', 'Проверки и результаты', 'Работа с утилитой'),
    ('troubleshooting', 'Диагностика', 'Работа с утилитой'),
]


def slug(text):
    text = unicodedata.normalize('NFKC', re.sub(r'[`*]', '', text)).lower()
    return re.sub(r'[^\w-]', '', re.sub(r'\s+', '-', text)).strip('-')


def inline(text, link_base):
    saved = []

    def stash(value):
        saved.append(value)
        return f'\x00{len(saved)-1}\x00'

    text = re.sub(r'`([^`]+)`', lambda m: stash('<code>' + html.escape(m[1]) + '</code>'), text)
    text = html.escape(text)

    def link(match):
        label, url = match[1], html.unescape(match[2])
        if url.endswith('.md'):
            name = Path(url).stem
            url = (link_base if name == 'index' else link_base + name + '/')
        if not (url.startswith(('https://', 'http://', '#', '../', './'))):
            raise ValueError('Unsupported link: ' + url)
        return '<a href="' + html.escape(url, quote=True) + '">' + label + '</a>'

    text = re.sub(r'\[([^\]]+)\]\(([^)]+)\)', link, text)
    text = re.sub(r'\*\*([^*]+)\*\*', r'<strong>\1</strong>', text)
    text = re.sub(r'\x00(\d+)\x00', lambda m: saved[int(m[1])], text)
    return text


def markdown(source, link_base):
    """Render the documented Markdown subset: headings, code, lists, tables, quotes."""
    lines, out, toc, seen = source.splitlines(), [], [], set()
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip():
            i += 1
            continue
        if line.startswith('```'):
            language = line[3:].strip()
            block = []
            i += 1
            while i < len(lines) and not lines[i].startswith('```'):
                block.append(lines[i])
                i += 1
            if i == len(lines):
                raise ValueError('Unclosed code block')
            out.append('<pre data-copy><code class="language-' + html.escape(language) + '">' + html.escape('\n'.join(block)) + '</code></pre>')
        elif line.startswith('#'):
            match = re.fullmatch(r'(#{1,3})\s+(.+)', line)
            if not match:
                raise ValueError('Unsupported heading: ' + line)
            level, title = len(match[1]), match[2]
            anchor = slug(title)
            if anchor in seen:
                raise ValueError('Duplicate heading: ' + title)
            seen.add(anchor)
            out.append(f'<h{level} id="{anchor}">{inline(title,link_base)}</h{level}>')
            if level == 2:
                toc.append((anchor, title))
        elif line.startswith('|'):
            rows = []
            while i < len(lines) and lines[i].startswith('|'):
                rows.append([cell.strip() for cell in lines[i].strip('|').split('|')])
                i += 1
            head, body = rows[0], rows[2:]
            out.append('<div class="table-scroll"><table><thead><tr>' + ''.join('<th>' + inline(c, link_base) + '</th>' for c in head) + '</tr></thead><tbody>')
            for row in body:
                if len(row) != len(head):
                    raise ValueError('Uneven table')
                out.append('<tr>' + ''.join('<td>' + inline(c,link_base) + '</td>' for c in row) + '</tr>')
            out.append('</tbody></table></div>')
            continue
        elif re.match(r'^(- |\d+\. )', line):
            ordered = bool(re.match(r'^\d+\.', line))
            tag = 'ol' if ordered else 'ul'
            out.append('<' + tag + '>')
            pattern = r'^\d+\. (.*)' if ordered else r'^- (.*)'
            while i < len(lines):
                match = re.match(pattern, lines[i])
                if not match:
                    break
                out.append('<li>' + inline(match[1],link_base) + '</li>')
                i += 1
            out.append('</' + tag + '>')
            continue
        elif line.startswith('> '):
            out.append('<blockquote><p>' + inline(line[2:],link_base) + '</p></blockquote>')
        else:
            paragraph = [line]
            i += 1
            while i < len(lines) and lines[i].strip() and not re.match(r'^(#|```|\||- |\d+\. |> )', lines[i]):
                paragraph.append(lines[i])
                i += 1
            out.append('<p>' + inline(' '.join(paragraph),link_base) + '</p>')
            continue
        i += 1
    return '\n'.join(out), toc


def page_url(name):
    return '../' if name == 'index' else '../' + name + '/'


def build(destination, repo):
    destination = destination.resolve()
    if destination in (ROOT, ROOT / 'website') or ROOT not in destination.parents:
        raise ValueError('Output must be a separate directory inside the project')
    if destination.exists():
        # Only our generated output may be replaced.
        if not (destination / '.k8nnect-generated').exists():
            raise ValueError('Refusing to replace an unowned output directory')
        shutil.rmtree(destination)
    destination.mkdir(parents=True)
    (destination / '.k8nnect-generated').touch()
    (destination / '.nojekyll').touch()
    shutil.copytree(ROOT / 'website/assets', destination / 'assets')
    landing = (ROOT / 'website/index.html').read_text().replace('{{repo}}', html.escape(repo, quote=True))
    (destination / 'index.html').write_text(landing)
    for index, (name, title, _) in enumerate(PAGES):
        prefix = '../' if name == 'index' else '../../'
        target = destination / 'docs' / ('' if name == 'index' else name)
        target.mkdir(parents=True, exist_ok=True)
        article, toc = markdown((ROOT / 'website/content' / (name + '.md')).read_text(), './' if name == 'index' else '../')
        nav, previous_group = [], None
        for key, label, group in PAGES:
            url = ('./' if key == 'index' else key + '/') if name == 'index' else page_url(key)
            if group != previous_group:
                if previous_group is not None:
                    nav.append('</div>')
                nav.append('<div class="nav-group"><div class="group">' + group + '</div>')
                previous_group = group
            nav.append('<a data-search="' + html.escape(label) + '" href="' + url + '"' + (' aria-current="page"' if key == name else '') + '>' + label + '</a>')
        nav.append('</div>')
        contents = ''.join('<a href="#' + anchor + '">' + html.escape(label) + '</a>' for anchor, label in toc)
        pagination = ''
        if index > 0:
            key, label, _ = PAGES[index-1]
            url = page_url(key) if name != 'index' else key + '/'
            pagination += '<a href="' + url + '">← ' + label + '</a>'
        if index+1 < len(PAGES):
            key, label, _ = PAGES[index+1]
            url = page_url(key) if name != 'index' else key + '/'
            pagination += '<a href="' + url + '">' + label + ' →</a>'
        page = f'''<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{title} — k8nnect</title><meta name="description" content="{title}: руководство k8nnect для подключения внешней VDS к Kubernetes."><meta name="theme-color" content="#126b56"><link rel="icon" href="{prefix}assets/logo.svg" type="image/svg+xml"><link rel="stylesheet" href="{prefix}assets/style.css"><script defer src="{prefix}assets/app.js"></script></head><body>
<a class="skip" href="#main">Перейти к содержимому</a><header class="header"><div class="wrap"><a class="brand" href="{prefix}" aria-label="k8nnect — главная"><img src="{prefix}assets/logo.svg" width="34" height="34" alt="">k8nnect</a><nav class="nav" aria-label="Главное меню"><a href="{prefix}docs/">Документация</a><a href="{repo}">GitHub ↗</a><span class="pill">Experimental</span></nav></div></header>
<div class="wrap docs-shell"><button class="docs-menu" aria-expanded="false" aria-controls="docs-sidebar">Разделы документации ↓</button><aside class="sidebar" id="docs-sidebar" aria-label="Разделы документации"><label class="sr-only" for="doc-search">Найти раздел</label><input class="doc-search" id="doc-search" type="search" placeholder="Найти раздел…"><nav>{''.join(nav)}</nav><p class="search-empty" hidden>Раздел не найден</p></aside><main class="doc-article" id="main"><div class="eyebrow">k8nnect / documentation</div>{article}<nav class="doc-end" aria-label="Соседние страницы">{pagination}</nav></main><aside class="toc" aria-label="На этой странице"><strong>На этой странице</strong>{contents}</aside></div><footer class="footer"><div class="wrap"><span>k8nnect · Experimental · k0s</span><div><a href="{repo}">Исходный код ↗</a><a href="{repo}/blob/main/website/content/{name}.md">Изменить страницу ↗</a></div></div></footer></body></html>'''
        (target / 'index.html').write_text(page)
    (destination / '404.html').write_text('<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Страница не найдена — k8nnect</title><body style="font:18px/1.7 system-ui;max-width:600px;margin:12vh auto;padding:24px"><h1>Страница не найдена</h1><p>Возможно, адрес документации изменился.</p><a href="' + repo + '">Открыть репозиторий k8nnect →</a></body></html>')
    print(f'Built {len(PAGES)+2} pages in {destination}')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', default='site')
    parser.add_argument('--repo', default='https://github.com/kizilov-dev/k8nnect')
    args = parser.parse_args()
    if not re.fullmatch(r'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', args.repo):
        parser.error('--repo must be a GitHub repository URL')
    build(ROOT / args.output, args.repo)
