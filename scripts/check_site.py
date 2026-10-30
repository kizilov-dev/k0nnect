#!/usr/bin/env python3
"""Check the generated site for broken local links, anchors and unresolved templates."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlsplit, unquote
import re

root=Path(__file__).resolve().parent.parent/'site'
class Document(HTMLParser):
    def __init__(self):
        super().__init__(); self.ids=set(); self.links=[]; self.h1=0; self.lang=None
    def handle_starttag(self,tag,attrs):
        a=dict(attrs)
        if tag=='html': self.lang=a.get('lang')
        if tag=='h1': self.h1+=1
        if 'id' in a:self.ids.add(a['id'])
        for key in ('href','src'):
            if key in a:self.links.append(a[key])
docs={}
for p in root.rglob('*.html'):
    text=p.read_text()
    assert not re.search(r'[А-Яа-яЁё]', text), f'Untranslated content in {p}'
    assert 'k8nnect' not in text.lower(), f'Outdated branding in {p}'
    assert '{{' not in text, f'Unresolved template in {p}'
    doc=Document();doc.feed(text);docs[p.resolve()]=doc
    assert doc.h1==1,f'Expected one H1: {p}'
    assert doc.lang=='en',f'Missing lang: {p}'
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
