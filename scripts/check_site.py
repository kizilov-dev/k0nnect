#!/usr/bin/env python3
"""Check the generated site for broken local links, anchors and unresolved templates."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlsplit, unquote
import re

root=Path(__file__).resolve().parent.parent/'site'
class Document(HTMLParser):
    def __init__(self):
        super().__init__(); self.ids=set(); self.links=[]; self.h1=0; self.lang=None; self.languages=[]
    def handle_starttag(self,tag,attrs):
        a=dict(attrs)
        if tag=='html': self.lang=a.get('lang')
        if tag=='option':
            self.links.append(a['value']); self.languages.append((a.get('lang'), 'selected' in a))
        if tag=='h1': self.h1+=1
        if 'id' in a:self.ids.add(a['id'])
        for key in ('href','src'):
            if key in a:self.links.append(a[key])
docs={}
for p in root.rglob('*.html'):
    text=p.read_text()
    assert 'k8nnect' not in text.lower(), f'Outdated branding in {p}'
    assert '{{' not in text, f'Unresolved template in {p}'
    doc=Document();doc.feed(text);docs[p.resolve()]=doc
    assert doc.h1==1,f'Expected one H1: {p}'
    assert doc.lang in ('en','ru'),f'Missing lang: {p}'
    if doc.lang == 'en':
        assert not re.search(r'[А-Яа-яЁё]', re.sub(r'<option[^>]*>.*?</option>', '', text)), f'Untranslated content in {p}'
    if p.name != '404.html':
        assert doc.languages == [('en',doc.lang=='en'),('ru',doc.lang=='ru')], f'Invalid language switch: {p}'
assert {doc.lang for doc in docs.values()} == {'en','ru'}, 'Both languages must be built'
for p,doc in docs.items():
    for link in doc.links:
        url=urlsplit(link)
        if url.scheme or url.netloc:continue
        target=(p.parent/unquote(url.path)).resolve() if url.path else p
        if target.is_dir():target=target/'index.html'
        assert target.is_relative_to(root.resolve()),f'Link escapes project base: {p}: {link}'
        assert target.is_file(),f'Broken link: {p}: {link}'
        if url.fragment:
            assert unquote(url.fragment) in docs[target].ids,f'Broken anchor: {p}: {link}'
print(f'PASS: {len(docs)} pages, all local assets, relative links and anchors; project-path safe')
