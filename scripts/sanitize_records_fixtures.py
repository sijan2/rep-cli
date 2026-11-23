#!/usr/bin/env python3
"""Sanitize private live captures into decoder fixtures for internal/records.

The raw captures (one Reddit thread document in two sorts and one X
TweetDetail GraphQL response) hold public third-party content plus
viewer-specific tokens, so they stay outside the repository. This script keeps
only what a decoder needs, remaps every identifier consistently, replaces every
text with same-length filler and drops viewer-specific data. It never prints
raw content: every report is a count.

Generate the sanitized fixtures, recompute facts.json and verify privacy:

  python3 -I scripts/sanitize_records_fixtures.py \\
      --reddit-ssr RAW_SSR.html --reddit-new RAW_NEW.html \\
      --x-tweetdetail RAW_TD.json [--x-extra RAW_OTHER.json] \\
      --out internal/records/testdata

Verify only (writes nothing; exit status 1 on any leak):

  python3 -I scripts/sanitize_records_fixtures.py --verify <same raw flags> --out DIR

Recompute facts.json from the fixtures alone (for example after editing a
*.synthetic.* fixture):

  python3 -I scripts/sanitize_records_fixtures.py --facts-only --out DIR

facts.json is computed from the fixture files themselves with a regex tag scan
(HTML) or a JSON walk (X), independently of the transformation code, and the
sanitized fixtures' preserved counts are cross-checked against the same scan
of the raw input. Standard library only; deterministic for the same inputs.
"""

import argparse
import base64
import collections
import datetime
import hashlib
import html
import json
import os
import re
import sys

DAY_SHIFT = datetime.timedelta(days=-728)  # 104 weeks back: weekday names stay valid
DAY_SHIFT_MS = int(DAY_SHIFT.total_seconds() * 1000)
SYNTH_AVATAR = 'https://www.redditstatic.com/avatars/defaults/v2/avatar_default_0.png'
SYNTH_ICON = 'https://www.redditstatic.com/avatars/defaults/v2/avatar_default_1.png'

OUT_REDDIT_SSR = 'reddit_thread_ssr.html'
OUT_REDDIT_NEW = 'reddit_thread_ssr_new.html'
OUT_X = 'x_tweetdetail.json'
OUT_FACTS = 'facts.json'

# The synthetic more-comments fragment is the response to one branch loader of
# the sanitized SSR fixture (named by its synthetic cursor).
FRAGMENT_ANSWERS = {
    'reddit_more_comments.synthetic.html': (OUT_REDDIT_SSR, 'cursor_17'),
}

# Page classes the hand-written synthetic fixtures are built to exercise. The
# scan below derives a class too; facts.json records both so a disagreement is
# visible.
SYNTHETIC_EXPECT = {
    'reddit_more_comments.synthetic.html': {'class': 'ok'},
    'reddit_gated.synthetic.html': {'class': 'gated', 'reason': 'over18'},
    'reddit_challenge.synthetic.html': {'class': 'challenge'},
    'x_tweetdetail_showmore.synthetic.json': {'class': 'ok', 'terminal_bottom': True},
    'x_tweetdetail_unknown.synthetic.json': {'class': 'drift_unknown'},
}


def log(msg):
    print(msg, file=sys.stderr)


# --------------------------------------------------------------------- filler

_VOCAB = (
    'a e o do ut et ad ex ea in id sit sed qui non est amet elit enim quis nisi '
    'duis aute esse sint sunt anim lorem ipsum dolor magna minim irure velit '
    'nulla culpa tempor labore dolore aliqua veniam fugiat cillum mollit nostrud '
    'ullamco laboris aliquip commodo officia laborum eiusmod pariatur proident '
    'deserunt occaecat consequat voluptate excepteur cupidatat adipiscing '
    'incididunt consectetur exercitation reprehenderit').split()
_BY_LEN = collections.defaultdict(list)
for _w in _VOCAB:
    _BY_LEN[len(_w)].append(_w)
_STREAM = ''.join(_VOCAB)
_ENTITY = re.compile(r'&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});')


def filler_word(length, pos):
    """A filler word of exactly `length` letters; depends only on length and position."""
    words = _BY_LEN.get(length)
    if words:
        return words[(pos * 7 + length) % len(words)]
    start = (pos * 13) % len(_STREAM)
    s = _STREAM[start:] + _STREAM
    while len(s) < length:
        s += _STREAM
    return s[:length]


def fill_run(run, pos):
    """Replace an alphanumeric run, keeping its length and per-character class."""
    word = filler_word(len(run), pos)
    out = []
    for j, ch in enumerate(run):
        if ch.isdigit():
            out.append(str((pos + j) % 10))
        elif ch.isupper():
            out.append(word[j].upper())
        else:
            out.append(word[j])
    return ''.join(out)


def fill_text(raw, keep_entities=True):
    """Same-length filler: alphanumeric runs replaced; whitespace, punctuation and
    character references kept, so decoded length and paragraph shape survive.
    The filler depends on positions and character classes only, never on the
    original letters."""
    out = []
    i, n, pos = 0, len(raw), 0
    while i < n:
        ch = raw[i]
        if ch == '&' and keep_entities:
            m = _ENTITY.match(raw, i)
            if m:
                out.append(m.group(0))
                i = m.end()
                pos += 1
                continue
        if ch.isalnum():
            j = i
            while j < n and raw[j].isalnum():
                j += 1
            out.append(fill_run(raw[i:j], pos))
            pos += j - i
            i = j
            continue
        out.append(ch)
        i += 1
        pos += 1
    return ''.join(out)


_ISO_DATE = re.compile(r'(\d{4})-(\d{2})-(\d{2})(?=T\d{2}:\d{2})')
_X_TIME = '%a %b %d %H:%M:%S %z %Y'


def shift_iso(value):
    def rep(m):
        d = datetime.date(int(m.group(1)), int(m.group(2)), int(m.group(3))) + DAY_SHIFT
        return d.isoformat()
    return _ISO_DATE.sub(rep, value)


def shift_x_time(value):
    t = datetime.datetime.strptime(value, _X_TIME) + DAY_SHIFT
    return t.strftime(_X_TIME)


# ------------------------------------------------------------------ HTML tree

VOID = frozenset('area base br col embed hr img input link meta param source track wbr keygen'.split())
RAWTEXT = frozenset('script style textarea title xmp iframe noembed noframes noscript plaintext'.split())
_TAG_RE = re.compile(
    r'<(/?)([A-Za-z][A-Za-z0-9:_.-]*)'
    r'((?:\s+[^\s=/>"\']+(?:\s*=\s*(?:"[^"]*"|\'[^\']*\'|[^\s>"\']+))?)*)'
    r'\s*(/?)>', re.S)
_ATTR_RE = re.compile(r'([^\s=/>"\']+)(?:\s*=\s*(?:"([^"]*)"|\'([^\']*)\'|([^\s>"\']+)))?')
_RAW_CLOSE = {}


class Node:
    __slots__ = ('kind', 'tag', 'name', 'attrs', 'children', 'parent', 'raw')

    def __init__(self, kind, tag='', name='', attrs=None, raw=''):
        self.kind = kind          # root | el | text | comment | doctype
        self.tag = tag            # lowercase tag name
        self.name = name          # tag name as written
        self.attrs = attrs if attrs is not None else []  # (name, raw value or None, quote)
        self.children = []
        self.parent = None
        self.raw = raw

    def get(self, key, default=None):
        for k, v, _ in self.attrs:
            if k == key:
                return v
        lk = key.lower()
        for k, v, _ in self.attrs:
            if k.lower() == lk:
                return v
        return default

    def has(self, key):
        lk = key.lower()
        return any(k.lower() == lk for k, _, _ in self.attrs)

    def text(self):
        return ''.join(c.raw for c in self.children if c.kind == 'text')


def parse_attrs(s):
    out = []
    for m in _ATTR_RE.finditer(s):
        name, dq, sq, uq = m.groups()
        if dq is not None:
            out.append((name, dq, '"'))
        elif sq is not None:
            out.append((name, sq, "'"))
        elif uq is not None:
            out.append((name, uq, '"'))
        else:
            out.append((name, None, None))
    return out


def parse_html(src):
    """A tolerant tree builder that keeps raw attribute names, values and text."""
    root = Node('root')
    stack = [root]
    pos, n = 0, len(src)

    def add(node):
        node.parent = stack[-1]
        stack[-1].children.append(node)

    while pos < n:
        lt = src.find('<', pos)
        if lt < 0:
            add(Node('text', raw=src[pos:]))
            break
        if lt > pos:
            add(Node('text', raw=src[pos:lt]))
        if src.startswith('<!--', lt):
            end = src.find('-->', lt + 4)
            end = n if end < 0 else end + 3
            add(Node('comment', raw=src[lt:end]))
            pos = end
            continue
        if src.startswith('<!', lt) or src.startswith('<?', lt):
            end = src.find('>', lt)
            end = n if end < 0 else end + 1
            add(Node('doctype', raw=src[lt:end]))
            pos = end
            continue
        m = _TAG_RE.match(src, lt)
        if m is None:
            add(Node('text', raw='<'))
            pos = lt + 1
            continue
        closing, name, attrstr, selfclose = m.groups()
        tag = name.lower()
        pos = m.end()
        if closing:
            for i in range(len(stack) - 1, 0, -1):
                if stack[i].tag == tag:
                    del stack[i:]
                    break
            continue
        node = Node('el', tag, name, parse_attrs(attrstr))
        add(node)
        if tag in RAWTEXT:
            rx = _RAW_CLOSE.setdefault(tag, re.compile(r'</' + re.escape(tag) + r'\s*>', re.I))
            close = rx.search(src, pos)
            end = close.start() if close else n
            if end > pos:
                t = Node('text', raw=src[pos:end])
                t.parent = node
                node.children.append(t)
            pos = close.end() if close else n
            continue
        if tag not in VOID and not selfclose:
            stack.append(node)
    return root


def iter_elements(node):
    stack = [node]
    while stack:
        cur = stack.pop()
        if cur.kind == 'el':
            yield cur
        stack.extend(reversed(cur.children))


def iter_nodes(node):
    stack = [node]
    while stack:
        cur = stack.pop()
        yield cur
        stack.extend(reversed(cur.children))


def render_start(name, attrs):
    parts = ['<', name]
    for k, v, q in attrs:
        if v is None:
            parts.append(' ' + k)
            continue
        q = q or '"'
        if q in v:
            q = "'" if q == '"' else '"'
        parts.append(' %s=%s%s%s' % (k, q, v, q))
    parts.append('>')
    return ''.join(parts)


# ------------------------------------------------------------ Reddit sanitizer

_PREFIXED_ID = re.compile(r'(?<![A-Za-z0-9])t([1-6])_([a-z0-9]+)')
_SLUG = re.compile(r'(/comments/[a-z0-9]+/)(?!comment/)([^/?#"\'\s&<>]+)(?=/)')
_PARTIAL_HASH = re.compile(r'(/svc/shreddit/partial/)([A-Za-z0-9_-]{3,16})(?=/)')
_PARTIAL_NAME = re.compile(r'^([A-Za-z]+)_([A-Za-z0-9]{3,16})$')
_USER_PATH = re.compile(r'(/(?:u|user)/|user-hover-card/)([A-Za-z0-9_-]+)')
_CHROME_PARTIAL = re.compile(r'/(challenge-prompt|human-verification-dialog|banned-user-banner)(?=[/?]|$)')
_UI_TEXT = re.compile(r'^\s*(?:\d+\s+)?(?:more repl(?:y|ies)|continue this thread)\s*$', re.I)
_TIME_ATTRS = frozenset(['created', 'created-timestamp', 'ts', 'datetime', 'comment-created-timestamp'])
_FREE_TEXT_ATTRS = frozenset(['arialabel', 'aria-label', 'title', 'alt', 'label'])

# Subtrees that never hold anything a decoder needs (header, user drawer, chat,
# inbox, composer, ads, hover cards, action rows, scripts, styles, icons).
_DROP_TAGS = frozenset("""
script style svg noscript link meta iframe img picture video source
reddit-header-large header shreddit-comment-tree-ads shreddit-comments-page-ad
shreddit-comment-tree-ad shreddit-sidebar-ad comment-body-header
comment-composer-host shreddit-composer faceplate-form shreddit-comment-action-row
rpl-hovercard faceplate-hovercard ac-track ac-render-template faceplate-loader
shreddit-comment-badges author-flair-event-handler faceplate-timeago
""".split())
# Elements dropped inside kept loader and anchor subtrees.
_UI_DROP = frozenset(['script', 'style', 'svg', 'ac-track', 'img'])
_WRAP_ATTRS = frozenset([
    'id', 'slot', 'for', 'open', 'role', 'target', 'template', 'lang', 'bundlename',
    'data-more-replies-link', 'data-comment-section-anchor'])
_WRAP_EXTRA = {
    'shreddit-app': frozenset(['routename', 'pagetype', 'user-logged-in']),
    'section': frozenset(['aria-label']),
}
_BODY_ATTRS = {
    'div': frozenset(['id', 'class', 'dir', 'slot']),
    'a': frozenset(['href', 'rel', 'target', 'class', 'rpl']),
    'ol': frozenset(['start']),
}


class RedditSanitizer:
    """Shared state for both Reddit documents, so a comment, author or post keeps
    one synthetic identity across the two sorts."""

    LETTER = {'1': 'c', '2': 'u', '3': 'p', '4': 'm', '5': 's', '6': 'w'}

    def __init__(self):
        self.ids = {}            # raw base36 id -> synthetic base36 id
        self.kind = {}           # raw base36 id -> prefix digit
        self.counters = collections.Counter()
        self.viewer = set()      # raw base36 of the signed-in viewer
        self.authors = {}        # raw name -> u<n>
        self.cursors = {}        # raw cursor -> cursor_<n>
        self.slugs = {}
        self.titles = {}
        self.streams = {}
        self.hashes = {}
        self.subs = {'programming': 'programming'}
        self.links = 0
        self._bare = (None, -1)
        self._names = (None, -1)
        self.stats = collections.Counter()

    # identity maps ---------------------------------------------------------
    def map_id(self, digit, raw):
        if raw in self.ids:
            return self.ids[raw]
        if digit == '2' and raw in self.viewer:
            self.counters['viewer'] += 1
            new = 'v%06d' % self.counters['viewer']
        else:
            self.counters[digit] += 1
            new = '%s%06d' % (self.LETTER.get(digit, 'z'), self.counters[digit])
        self.ids[raw] = new
        self.kind[raw] = digit
        return new

    def map_author(self, name):
        if name in ('', '[deleted]', '[removed]'):
            return name
        if name not in self.authors:
            self.authors[name] = 'u%d' % (len(self.authors) + 1)
        return self.authors[name]

    def map_cursor(self, value):
        if value not in self.cursors:
            self.cursors[value] = 'cursor_%d' % (len(self.cursors) + 1)
        return self.cursors[value]

    def map_slug(self, slug):
        if slug not in self.slugs:
            self.slugs[slug] = fill_text(slug)
        return self.slugs[slug]

    def map_title(self, title):
        if title not in self.titles:
            self.titles[title] = fill_text(title)
        return self.titles[title]

    def map_stream(self, value):
        if value not in self.streams:
            self.streams[value] = 'stream%d' % (len(self.streams) + 1)
        return self.streams[value]

    def map_hash(self, value):
        if value not in self.hashes:
            self.hashes[value] = 'hash%02d' % (len(self.hashes) + 1)
        return self.hashes[value]

    def map_sub(self, name):
        if name not in self.subs:
            self.subs[name] = 'sub%d' % len(self.subs)
        return self.subs[name]

    # regex helpers -----------------------------------------------------------
    def bare_re(self):
        rx, n = self._bare
        if n != len(self.ids):
            keys = sorted((k for k, d in self.kind.items() if d in ('1', '3')), key=len, reverse=True)
            rx = re.compile(r'(?<![a-z0-9])(?:%s)(?![a-z0-9])' % '|'.join(map(re.escape, keys))) if keys else None
            self._bare = (rx, len(self.ids))
        return rx

    def names_re(self):
        rx, n = self._names
        if n != len(self.authors):
            keys = sorted(self.authors, key=len, reverse=True)
            rx = re.compile(r'(?<![A-Za-z0-9_-])(?:%s)(?![A-Za-z0-9_-])' % '|'.join(map(re.escape, keys))) if keys else None
            self._names = (rx, len(self.authors))
        return rx

    def sub_ids(self, v):
        v = _PREFIXED_ID.sub(lambda m: 't%s_%s' % (m.group(1), self.map_id(m.group(1), m.group(2))), v)
        rx = self.bare_re()
        if rx is not None:
            v = rx.sub(lambda m: self.ids[m.group(0)], v)
        return v

    def sub_names(self, v):
        rx = self.names_re()
        return rx.sub(lambda m: self.authors[m.group(0)], v) if rx is not None else v

    def generic(self, lname, v):
        v = _PARTIAL_HASH.sub(lambda m: m.group(1) + self.map_hash(m.group(2)), v)
        if lname == 'name':
            m = _PARTIAL_NAME.match(v)
            if m:
                v = m.group(1) + '_' + self.map_hash(m.group(2))
        v = _SLUG.sub(lambda m: m.group(1) + self.map_slug(m.group(2)), v)
        v = self.sub_ids(v)
        for raw, new in self.streams.items():
            if raw in v:
                v = v.replace(raw, new)
        v = _USER_PATH.sub(lambda m: m.group(1) + self.map_author(m.group(2)), v)
        if lname in _FREE_TEXT_ATTRS:
            v = self.sub_names(v)
        return v

    def body_href(self, v):
        raw = html.unescape(v)
        m = re.match(r'^/(u|user)/([A-Za-z0-9_-]+)', raw)
        if m:
            return '/%s/%s/' % (m.group(1), self.map_author(m.group(2)))
        m = re.match(r'^/r/([A-Za-z0-9_]+)', raw)
        if m:
            return '/r/%s/' % self.map_sub(m.group(1))
        self.links += 1
        return 'https://example.com/link/%d' % self.links

    # attributes --------------------------------------------------------------
    def san_value(self, node, lname, v, ctx):
        if lname in ('author', 'author-name'):
            return self.map_author(v)
        if lname in ('avatar', 'author-avatar-url'):
            return SYNTH_AVATAR
        if lname == 'icon':
            return SYNTH_ICON
        if lname == 'post-title':
            return self.map_title(v)
        if lname == 'content-href':
            return 'https://example.com/article'
        if lname == 'domain':
            return 'example.com'
        if lname == 'site-key':
            return 'synthetic-site-key'
        if lname in _TIME_ATTRS:
            return shift_iso(v)
        if node.tag == 'input' and lname == 'value':
            if (node.get('name') or '') == 'cursor':
                return self.map_cursor(v)
            return 'synthetic'
        if ctx == 'body' and lname == 'href':
            return self.body_href(v)
        if node.tag == 'faceplate-partial' and lname == 'src' and _CHROME_PARTIAL.search(v) and '?' in v:
            path, _, query = v.partition('?')
            sep = '&amp;' if '&amp;' in query else '&'
            names = [kv.split('=', 1)[0] for kv in query.split(sep)]
            return self.generic(lname, path) + '?' + sep.join('%s=synthetic' % k for k in names)
        return self.generic(lname, v)

    def san_attrs(self, node, ctx):
        out = []
        for name, value, quote in node.attrs:
            lname = name.lower()
            if lname == 'nonce':
                continue
            if ctx == 'wrap':
                if lname not in _WRAP_ATTRS and lname not in _WRAP_EXTRA.get(node.tag, ()):
                    continue
            elif ctx == 'body':
                if lname not in _BODY_ATTRS.get(node.tag, ()):
                    continue
            if value is None:
                out.append((name, None, None))
                continue
            out.append((name, self.san_value(node, lname, value, ctx), quote))
        return out

    # selection and emission --------------------------------------------------
    def precollect(self, root):
        for el in iter_elements(root):
            t = el.tag
            if t == 'shreddit-post':
                pid = el.get('id') or ''
                if pid.startswith('t3_'):
                    self.map_id('3', pid[3:])
                uid = el.get('user-id') or ''
                if uid.startswith('t2_'):
                    self.viewer.add(uid[3:])
                    self.map_id('2', uid[3:])
                for key in ('author-id', 'subreddit-id'):
                    m = _PREFIXED_ID.match(el.get(key) or '')
                    if m:
                        self.map_id(m.group(1), m.group(2))
                if el.get('author'):
                    self.map_author(el.get('author'))
                if el.get('post-title'):
                    self.map_title(el.get('post-title'))
            elif t == 'shreddit-comment':
                tid = el.get('thingId') or ''
                if tid.startswith('t1_'):
                    self.map_id('1', tid[3:])
                if el.get('author'):
                    self.map_author(el.get('author'))
            elif t == 'faceplate-partial' and '/more-comments/' in (el.get('src') or ''):
                for d in iter_elements(el):
                    if d.tag == 'input' and d.get('name') == 'cursor':
                        self.map_cursor(d.get('value') or '')
            elif t == 'template' and el.get('for'):
                self.map_stream(el.get('for'))

    def rule(self, node):
        t = node.tag
        if t == 'shreddit-comment':
            return 'comment'
        if t == 'div' and node.get('slot') == 'comment':
            return 'body'
        if t == 'faceplate-partial':
            src = node.get('src') or ''
            if '/more-comments/' in src:
                return 'ui'
            if _CHROME_PARTIAL.search(src):
                return 'tag'
            return 'drop'
        if t == 'a' and node.get('slot') == 'more-comments-permalink':
            return 'ui'
        if t == 'shreddit-post':
            return 'tag'
        if t in ('shreddit-comment-tree-stats', 'reputation-recaptcha', 'suspense-replace'):
            return 'tag'
        if t == 'span' and node.has('data-human-verification-contribution-dialog-root'):
            return 'keep'
        if t == 'title':
            return 'title'
        if t in _DROP_TAGS:
            return 'drop'
        if t == 'shreddit-comment-tree':
            return 'tree'
        return 'wrap'

    def element(self, node, ctx, inner):
        start = render_start(node.name, self.san_attrs(node, ctx))
        if node.tag in VOID:
            return [start]
        return [start] + inner + ['</%s>' % node.name]

    def children(self, node, fn):
        out, pending, emitted = [], None, False
        for ch in node.children:
            if ch.kind == 'text':
                if not ch.raw.strip():
                    pending = ch.raw
                continue
            pieces = fn(ch)
            if pieces:
                if pending is not None:
                    out.append(pending)
                out.extend(pieces)
                pending = None
                emitted = True
        if emitted and node.children and node.children[-1].kind == 'text' and not node.children[-1].raw.strip():
            out.append(node.children[-1].raw)
        return out

    def emit(self, node):
        if node.kind == 'doctype':
            return [node.raw]
        if node.kind != 'el':
            return []
        r = self.rule(node)
        if r == 'drop':
            self.stats['dropped_subtrees'] += 1
            return []
        if r == 'comment':
            self.stats['comments'] += 1
            return self.element(node, 'all', self.children(node, self.emit))
        if r == 'body':
            self.stats['bodies'] += 1
            return self.full(node, 'body')
        if r == 'ui':
            self.stats['loaders_and_anchors'] += 1
            return self.full(node, 'ui')
        if r == 'tag':
            self.stats['tag_only'] += 1
            return self.element(node, 'all', [])
        if r == 'keep':
            return self.element(node, 'all', self.children(node, self.emit))
        if r == 'title':
            return self.element(node, 'wrap', [self.title_text(node.text())])
        inner = self.children(node, self.emit)
        if not inner:
            return []
        return self.element(node, 'all' if r == 'tree' else 'wrap', inner)

    def full(self, node, mode):
        if node.kind == 'text':
            raw = node.raw
            if mode == 'ui' and (not raw.strip() or _UI_TEXT.match(raw)):
                return [raw]
            return [fill_text(raw)]
        if node.kind != 'el':
            return []
        if node.tag in _UI_DROP or (mode == 'body' and node.tag in _DROP_TAGS):
            return []
        inner = []
        for ch in node.children:
            inner.extend(self.full(ch, mode))
        return self.element(node, 'body' if mode == 'body' else 'all', inner)

    def title_text(self, raw):
        dec = html.unescape(raw)
        for orig, new in self.titles.items():
            d_orig = html.unescape(orig)
            if d_orig and d_orig in dec:
                return html.escape(dec.replace(d_orig, html.unescape(new)), quote=False)
        return fill_text(raw)

    def sanitize(self, src):
        root = parse_html(src)
        self.precollect(root)
        out = []
        for ch in root.children:
            if ch.kind == 'text':
                if not ch.raw.strip():
                    out.append(ch.raw)
                continue
            out.extend(self.emit(ch))
        return ''.join(out)


# --------------------------------------------------------------- X sanitizer

_MENTION = re.compile(r'(?<![A-Za-z0-9_])@([A-Za-z0-9_]{1,15})')
_TCO = re.compile(r'https?://t\.co/([A-Za-z0-9]+)')
_LONG_DIGITS = re.compile(r'\d{10,}')
_PROMO_HEX = re.compile(r'(promoted-tweet-\d+-)([0-9a-f]{8,})$')
SORT_BASE = 1900000000000001000
TWEET_BASE = 1900000000000000000
USER_BASE = 2900000000000000000
NOTE_BASE = 3900000000000000000


def x_instructions(obj):
    if isinstance(obj, list):
        return obj
    return obj['data']['threaded_conversation_with_injections_v2']['instructions']


def json_walk(o, path=()):
    """Yield (path, key, value, parent) for every dict entry and list item."""
    if isinstance(o, dict):
        for k, v in o.items():
            yield path, k, v, o
            yield from json_walk(v, path + (k,))
    elif isinstance(o, list):
        for i, v in enumerate(o):
            yield path, i, v, o
            yield from json_walk(v, path + (i,))


class XSanitizer:
    def __init__(self):
        self.tweets = {}
        self.users = {}
        self.handles = {}
        self.notes = {}
        self.promo = {}
        self.sort = {}
        self.other_numbers = {}
        self.cursor_n = collections.Counter()
        self.tco = 0
        self.impressions = 0
        self.stats = collections.Counter()

    def collect(self, obj):
        ins = x_instructions(obj)
        tweet_ids, sorts = set(), set()

        def add_entry_ids(s):
            for m in re.finditer(r'(?:conversationthread|tweet)-(\d+)', s):
                tweet_ids.add(m.group(1))

        for path, k, v, parent in json_walk(ins):
            if k == 'sortIndex' and isinstance(v, str):
                sorts.add(int(v))
            elif k in ('entryId', 'moduleEntryId', 'entry_id_to_replace') and isinstance(v, str):
                add_entry_ids(v)
            elif k in ('id_str', 'conversation_id_str', 'in_reply_to_status_id_str', 'quoted_status_id_str') \
                    and isinstance(v, str) and 'legacy' in path[-1:]:
                tweet_ids.add(v)
            elif k == 'rest_id' and isinstance(v, str) and parent.get('__typename') == 'Tweet':
                tweet_ids.add(v)
            elif k in ('allTweetIds', 'edit_tweet_ids') and isinstance(v, list):
                tweet_ids.update(x for x in v if isinstance(x, str))
            elif k == 'tweet' and isinstance(v, dict) and isinstance(v.get('rest_id'), str):
                tweet_ids.add(v['rest_id'])
        for rank, tid in enumerate(sorted(tweet_ids, key=int), 1):
            self.tweets[tid] = str(TWEET_BASE + rank)
        if sorts:
            top = max(sorts)
            ordered = sorted(sorts, reverse=True)
            for rank, s in enumerate(ordered):
                gap = top - s
                self.sort[str(s)] = str(SORT_BASE - (gap if gap < 10 ** 6 else rank))
        # users in document order (focal author first)
        for path, k, v, parent in json_walk(ins):
            if isinstance(v, dict) and v.get('__typename') == 'User' and isinstance(v.get('rest_id'), str):
                self.map_user(v['rest_id'])
                core = v.get('core') or {}
                if isinstance(core.get('screen_name'), str):
                    self.map_handle(core['screen_name'])

    def map_tweet(self, tid):
        if tid not in self.tweets:
            self.tweets[tid] = str(TWEET_BASE + len(self.tweets) + 1)
        return self.tweets[tid]

    def map_user(self, uid):
        if uid not in self.users:
            self.users[uid] = str(USER_BASE + len(self.users) + 1)
        return self.users[uid]

    def map_handle(self, name):
        key = name.lower()
        if key not in self.handles:
            self.handles[key] = 'u%d' % (len(self.handles) + 1)
        return self.handles[key]

    def map_note(self, nid):
        if nid not in self.notes:
            self.notes[nid] = str(NOTE_BASE + len(self.notes) + 1)
        return self.notes[nid]

    def map_number(self, s):
        if s in self.tweets:
            return self.tweets[s]
        if s in self.sort:
            return self.sort[s]
        if s not in self.other_numbers:
            self.other_numbers[s] = str(4900000000000000000 + len(self.other_numbers) + 1)
        return self.other_numbers[s]

    def cursor_value(self, ctype):
        key = (ctype or 'cursor').lower()
        self.cursor_n[key] += 1
        return 'x_cursor_%s_%d' % (key, self.cursor_n[key])

    def entry_id(self, s):
        suffix = ''
        m = _PROMO_HEX.search(s)
        if m:
            if m.group(2) not in self.promo:
                self.promo[m.group(2)] = 'feedface%08x' % (len(self.promo) + 1)
            s, suffix = s[:m.start(2)], self.promo[m.group(2)]
        return _LONG_DIGITS.sub(lambda m: self.map_number(m.group(0)), s) + suffix

    def text(self, s):
        """Filler for tweet text. Mentions become the mapped handle, t.co codes are
        replaced at the same length and everything else becomes same-length
        filler. Returns the text and a map from old to new code point offsets."""
        out, idx = [], [0] * (len(s) + 1)
        i, n, newlen = 0, len(s), 0

        def put(old_start, old_end, piece):
            nonlocal newlen
            for p in range(old_start, old_end):
                idx[p] = newlen + min(p - old_start, len(piece))
            out.append(piece)
            newlen += len(piece)

        while i < n:
            m = _MENTION.match(s, i) if s[i] == '@' and (i == 0 or not (s[i - 1].isalnum() or s[i - 1] == '_')) else None
            if m:
                put(i, m.end(), '@' + self.map_handle(m.group(1)))
                i = m.end()
                continue
            m = _TCO.match(s, i) if s[i] == 'h' else None
            if m:
                self.tco += 1
                code = ('s%0' + str(max(len(m.group(1)) - 1, 1)) + 'd') % self.tco
                put(i, m.end(), m.group(0)[:m.start(1) - m.start()] + code[-len(m.group(1)):])
                i = m.end()
                continue
            if s[i] == '&':
                m = _ENTITY.match(s, i)
                if m:
                    put(i, m.end(), m.group(0))
                    i = m.end()
                    continue
            if s[i].isalnum():
                j = i
                while j < n and s[j].isalnum():
                    j += 1
                put(i, j, fill_run(s[i:j], i))
                i = j
                continue
            put(i, i + 1, s[i])
            i += 1
        idx[n] = newlen
        return ''.join(out), idx

    def user(self, u):
        if not isinstance(u, dict):
            return {}
        o = {}
        handle = None
        core = u.get('core') or {}
        if isinstance(core.get('screen_name'), str):
            handle = self.map_handle(core['screen_name'])
        for k, v in u.items():
            if k == '__typename':
                o[k] = v
            elif k == 'id' and isinstance(u.get('rest_id'), str):
                o[k] = base64.b64encode(('User:' + self.map_user(u['rest_id'])).encode()).decode()
            elif k == 'rest_id':
                o[k] = self.map_user(v)
            elif k == 'core':
                c = {}
                for ck in v:
                    if ck == 'name':
                        c[ck] = 'Name ' + (handle or 'u0')
                    elif ck == 'screen_name':
                        c[ck] = handle
                o[k] = c
            else:
                self.stats['user_fields_dropped'] += 1
        return o

    def legacy(self, L):
        text_map = None
        if isinstance(L.get('full_text'), str):
            new_text, text_map = self.text(L['full_text'])
        o = {}
        for k, v in L.items():
            if k in ('bookmark_count', 'favorite_count', 'quote_count', 'reply_count', 'retweet_count',
                     'lang', 'is_quote_status'):
                o[k] = v
            elif k == 'created_at':
                o[k] = shift_x_time(v)
            elif k in ('conversation_id_str', 'id_str', 'in_reply_to_status_id_str', 'quoted_status_id_str'):
                o[k] = self.map_tweet(v)
            elif k in ('user_id_str', 'in_reply_to_user_id_str'):
                o[k] = self.map_user(v)
            elif k == 'in_reply_to_screen_name':
                o[k] = self.map_handle(v)
            elif k == 'full_text':
                o[k] = new_text
            elif k == 'display_text_range' and text_map is not None:
                o[k] = [text_map[min(max(int(x), 0), len(text_map) - 1)] for x in v]
            elif k == 'quoted_status_permalink' and isinstance(v, dict):
                qid = self.map_tweet(L.get('quoted_status_id_str', '0'))
                self.tco += 1
                o[k] = {'url': 'https://t.co/s%09d' % self.tco,
                        'expanded': 'https://twitter.com/i/web/status/%s' % qid,
                        'display': 'x.com/i/web/status/%s…' % qid[:7]}
            else:
                self.stats['legacy_fields_dropped'] += 1
        return o

    def tweet(self, t):
        o = {}
        for k, v in t.items():
            if k == '__typename':
                o[k] = v
            elif k == 'rest_id':
                o[k] = self.map_tweet(v)
            elif k == 'core':
                o[k] = {'user_results': {'result': self.user((v.get('user_results') or {}).get('result'))}}
            elif k == 'note_tweet':
                r = ((v.get('note_tweet_results') or {}).get('result')) or {}
                nr = {}
                for rk in r:
                    if rk == 'id':
                        raw = base64.b64decode(r['id']).decode('utf-8', 'replace')
                        nid = raw.split(':', 1)[1] if ':' in raw else raw
                        nr[rk] = base64.b64encode(('NoteTweet:' + self.map_note(nid)).encode()).decode()
                    elif rk == 'text':
                        nr[rk] = self.text(r['text'])[0]
                note = {}
                for nk in v:
                    if nk == 'is_expandable':
                        note[nk] = v[nk]
                    elif nk == 'note_tweet_results':
                        note[nk] = {'result': nr}
                o[k] = note
            elif k == 'legacy':
                o[k] = self.legacy(v)
            elif k == 'views':
                o[k] = {vk: v[vk] for vk in ('count', 'state') if vk in v}
            elif k == 'quoted_status_result':
                o[k] = {'result': self.tweet_result(v.get('result') or {})}
            else:
                self.stats['tweet_fields_dropped'] += 1
        return o

    def tweet_result(self, r):
        tn = r.get('__typename')
        if tn == 'TweetWithVisibilityResults':
            o = {'__typename': tn}
            if isinstance(r.get('tweet'), dict):
                o['tweet'] = self.tweet(r['tweet'])
            return o
        if tn == 'TweetTombstone':
            return {'__typename': tn, 'tombstone': r.get('tombstone', {})}
        if tn in (None, 'Tweet'):
            return self.tweet(r)
        self.stats['unknown_results'] += 1
        return {'__typename': tn}

    def promoted(self, p):
        o = {}
        for k, v in p.items():
            if k == 'advertiser_results':
                o[k] = {'result': self.user((v or {}).get('result'))}
            elif k in ('impressionId', 'impressionString'):
                self.impressions += 1
                o[k] = 'synthetic-impression-%d' % self.impressions
            elif k == 'disclosureType':
                o[k] = v
            else:
                self.stats['promoted_fields_dropped'] += 1
        return o

    def item_content(self, ic):
        o = {}
        for k, v in ic.items():
            if k in ('itemType', '__typename', 'tweetDisplayType', 'cursorType'):
                o[k] = v
            elif k == 'tweet_results':
                o[k] = {'result': self.tweet_result(v['result'])} if isinstance(v, dict) and 'result' in v else {}
            elif k == 'promotedMetadata':
                o[k] = self.promoted(v)
            elif k == 'value':
                o[k] = self.cursor_value(ic.get('cursorType'))
            elif k == 'displayTreatment' and isinstance(v, dict):
                o[k] = {dk: dv for dk, dv in v.items() if dk == 'actionText'}
            else:
                self.stats['item_fields_dropped'] += 1
        return o

    def module_item(self, x):
        o = {}
        for k, v in x.items():
            if k == 'entryId':
                o[k] = self.entry_id(v)
            elif k == 'item':
                o[k] = {'itemContent': self.item_content(v.get('itemContent') or {})}
        return o

    def content(self, c):
        o = {}
        for k, v in c.items():
            if k in ('entryType', '__typename', 'displayType', 'cursorType'):
                o[k] = v
            elif k == 'itemContent':
                o[k] = self.item_content(v)
            elif k == 'items':
                o[k] = [self.module_item(x) for x in v]
            elif k == 'metadata' and isinstance(v, dict):
                meta = {}
                cm = v.get('conversationMetadata')
                if isinstance(cm, dict):
                    nm = {}
                    for mk, mv in cm.items():
                        if mk == 'allTweetIds':
                            nm[mk] = [self.map_tweet(x) for x in mv]
                        elif mk == 'enableDeduplication':
                            nm[mk] = mv
                    meta['conversationMetadata'] = nm
                o[k] = meta
            elif k == 'value':
                o[k] = self.cursor_value(c.get('cursorType'))
            elif k == 'displayTreatment' and isinstance(v, dict):
                o[k] = {dk: dv for dk, dv in v.items() if dk == 'actionText'}
            else:
                self.stats['content_fields_dropped'] += 1
        return o

    def entry(self, e):
        o = {}
        for k, v in e.items():
            if k == 'entryId':
                o[k] = self.entry_id(v)
            elif k == 'sortIndex':
                o[k] = self.map_number(v)
            elif k == 'content':
                o[k] = self.content(v)
        return o

    def instruction(self, i):
        o = {}
        for k, v in i.items():
            if k in ('type', 'direction', 'prepend'):
                o[k] = v
            elif k == 'entries':
                o[k] = [self.entry(e) for e in v]
            elif k == 'entry':
                o[k] = self.entry(v)
            elif k in ('moduleEntryId', 'entry_id_to_replace'):
                o[k] = self.entry_id(v)
            elif k == 'moduleItems':
                o[k] = [self.module_item(x) for x in v]
            else:
                self.stats['instruction_fields_dropped'] += 1
        return o

    def sanitize(self, obj):
        self.collect(obj)
        if isinstance(obj, list):
            return [self.instruction(i) for i in obj]
        t = obj['data']['threaded_conversation_with_injections_v2']
        out = {}
        for k, v in t.items():
            if k == 'instructions':
                out[k] = [self.instruction(i) for i in v]
            elif k == 'metadata' and isinstance(v, dict):
                sc = v.get('scribeConfig')
                out[k] = {'scribeConfig': {'page': sc['page']}} if isinstance(sc, dict) and 'page' in sc else {}
        return {'data': {'threaded_conversation_with_injections_v2': out}}


# --------------------------------------------------- Reddit facts (regex scan)

_F_STRIP = re.compile(r'<!--.*?-->|<(script|style)\b[^>]*>.*?</\1\s*>', re.S | re.I)
_F_TAG = re.compile(
    r'<(/?)([A-Za-z][A-Za-z0-9-]*)((?:\s+[^\s=/>"\']+(?:\s*=\s*(?:"[^"]*"|\'[^\']*\'|[^\s>"\']+))?)*)\s*(/?)>', re.S)
_F_ATTR = re.compile(r'\s([^\s=/>"\']+)(?:\s*=\s*("[^"]*"|\'[^\']*\'|[^\s>"\']+))?')
_F_PRIVATE = re.compile(r'header-action-item-|user-drawer|reddit-chat|/chat\b|inbox|matrix\.redditspace')


def _fattrs(s):
    names, vals = [], {}
    for m in _F_ATTR.finditer(s):
        k, v = m.group(1), m.group(2)
        if v is not None and v[:1] in ('"', "'"):
            v = v[1:-1]
        names.append(k)
        vals.setdefault(k.lower(), v)
    return names, vals


def _int(v):
    return int(v) if isinstance(v, str) and re.fullmatch(r'-?\d+', v) else None


def _hist(values):
    c = collections.Counter(v for v in values if v is not None)
    return {str(k): c[k] for k in sorted(c)}


def reddit_scan(text):
    body = _F_STRIP.sub('', text)
    comments, loaders, anchors = [], [], []
    cstack, pstack, astack = [], [], []
    body_owner, body_depth, body_text = None, 0, []
    post = tree = stats = None
    partials = collections.Counter()
    camel = lower = 0
    prev = 0
    for m in _F_TAG.finditer(body):
        if body_owner is not None and m.start() > prev:
            body_text.append(body[prev:m.start()])
        prev = m.end()
        closing, name, attrstr, selfclose = m.groups()
        t = name.lower()
        if closing:
            if body_owner is not None and t == 'div':
                body_depth -= 1
                if body_depth == 0:
                    body_owner['texts'].append(''.join(body_text))
                    body_owner, body_text = None, []
            if t == 'shreddit-comment' and cstack:
                cstack.pop()
            elif t == 'faceplate-partial' and pstack:
                pstack.pop()
            elif t == 'a' and astack:
                astack.pop()
            continue
        names, A = _fattrs(attrstr)
        loader = pstack[-1] if pstack and pstack[-1] is not None else None
        anchor = astack[-1] if astack and astack[-1] is not None else None
        owner = cstack[-1] if cstack else None
        if body_owner is not None:
            if t == 'div' and not selfclose:
                body_depth += 1
            if t == 'a' and not selfclose:
                astack.append(None)
            continue
        if t == 'shreddit-comment':
            camel += names.count('thingId')
            lower += names.count('thingid')
            c = {'id': A.get('thingid'), 'parent': A.get('parentid'), 'post': A.get('postid'),
                 'depth': _int(A.get('depth')), 'author': A.get('author'),
                 'nest': owner['id'] if owner else None, 'bodies': 0, 'texts': []}
            comments.append(c)
            if not selfclose:
                cstack.append(c)
        elif t == 'div' and A.get('slot') == 'comment':
            target = owner if owner is not None else {'texts': [], 'bodies': 0}
            target['bodies'] += 1
            body_owner, body_depth, body_text = target, 1, []
        elif t == 'faceplate-partial':
            src = A.get('src') or ''
            if '/more-comments/' in src:
                L = {'loading': A.get('loading'), 'method': A.get('method'), 'id': A.get('id'),
                     'slot': A.get('slot'), 'src': src, 'owner': owner['id'] if owner else None,
                     'owner_depth': owner['depth'] if owner else None, 'cursors': [], 'inputs': 0,
                     'numbers': [], 'buttons': 0, 'nouns': []}
                loaders.append(L)
                if not selfclose:
                    pstack.append(L)
            else:
                cm = _CHROME_PARTIAL.search(src)
                if cm:
                    partials[cm.group(1)] += 1
                elif _F_PRIVATE.search(src):
                    partials['private'] += 1
                else:
                    partials['other'] += 1
                if not selfclose:
                    pstack.append(None)
        elif t == 'input' and loader is not None:
            loader['inputs'] += 1
            if A.get('name') == 'cursor' and A.get('type') == 'hidden':
                loader['cursors'].append(A.get('value'))
        elif t == 'faceplate-number':
            n = _int(A.get('number'))
            if loader is not None:
                loader['numbers'].append(n)
            elif anchor is not None:
                anchor['numbers'].append(n)
        elif t == 'faceplate-tracker':
            if loader is not None:
                loader['nouns'].append(A.get('noun'))
            elif anchor is not None:
                anchor['nouns'].append(A.get('noun'))
        elif t == 'button' and loader is not None:
            loader['buttons'] += 1
        elif t == 'a':
            if A.get('slot') == 'more-comments-permalink':
                an = {'href': A.get('href'), 'owner': owner['id'] if owner else None, 'numbers': [], 'nouns': []}
                anchors.append(an)
                if not selfclose:
                    astack.append(an)
            elif not selfclose:
                astack.append(None)
        elif t == 'shreddit-post':
            post = A
        elif t == 'shreddit-comment-tree':
            tree = A
        elif t == 'shreddit-comment-tree-stats':
            stats = A
    visible = html.unescape(re.sub(r'<[^>]*>', ' ', body)).lower()
    return {'comments': comments, 'loaders': loaders, 'anchors': anchors, 'post': post, 'tree': tree,
            'stats': stats, 'partials': partials, 'camel': camel, 'lower': lower, 'visible': visible,
            'over18': len(re.findall(r'/over18\b|name="over18"', body)),
            'quarantine': len(re.findall(r'quarantin', visible))}


def _nonspace(raw):
    return len(re.sub(r'\s', '', html.unescape(raw)))


def reddit_facts(text):
    s = reddit_scan(text)
    comments, loaders, anchors = s['comments'], s['loaders'], s['anchors']
    first = {}
    for c in comments:
        first.setdefault(c['id'], c)
    counts = collections.Counter(c['id'] for c in comments)
    in_doc = set(first)

    def derived(c, seen=()):
        p = c['parent']
        if not p:
            return 0
        if p not in first or p in seen:
            return None
        d = derived(first[p], seen + (c['id'],))
        return None if d is None else d + 1

    uniq = list(first.values())
    deleted = [c for c in uniq if c['author'] in ('[deleted]', '[removed]')]
    external = sorted({c['parent'] for c in uniq if c['parent'] and c['parent'] not in in_doc})
    f = {
        'comments': len(comments),
        'unique_thing_ids': len(first),
        'repeated_thing_ids': {k: v for k, v in sorted(counts.items()) if v > 1},
        'thingId_attrs_camelcase': s['camel'],
        'thingid_attrs_lowercase': s['lower'],
        'with_parentId': sum(1 for c in comments if c['parent']),
        'top_level': sum(1 for c in comments if not c['parent']),
        'top_level_with_t3_postId': sum(1 for c in comments if not c['parent'] and (c['post'] or '').startswith('t3_')),
        'wire_depth_histogram': _hist(c['depth'] for c in comments),
        'wire_depth_histogram_unique': _hist(c['depth'] for c in uniq),
        'nesting_parent_equals_parentId': sum(1 for c in comments if (c['nest'] or None) == (c['parent'] or None)),
        'parents_outside_document': external,
        'derived_depth_mismatches': sum(1 for c in uniq if derived(c) is not None and derived(c) != c['depth']),
        'derived_depth_unresolved': sum(1 for c in uniq if derived(c) is None),
        'deleted': len(deleted),
        'comments_with_body': sum(1 for c in comments if c['bodies']),
        'body_slots': sum(c['bodies'] for c in comments),
        'records': [{'id': c['id'], 'parent': c['parent'] or c['post'], 'wire_depth': c['depth'],
                     'author': c['author'], 'text_nonspace_chars': sum(_nonspace(x) for x in c['texts'])}
                    for c in uniq],
    }
    post = s['post'] or {}
    f['expected'] = {
        'shreddit_post_comment_count': _int(post.get('comment-count')),
        'comment_tree_totalComments': _int((s['tree'] or {}).get('totalcomments')),
        'comment_tree_stats_total_comments': _int((s['stats'] or {}).get('total-comments')),
    }
    f['post'] = {'present': s['post'] is not None, 'id': post.get('id'), 'author': post.get('author')} if s['post'] else {'present': False}

    branch_owners = set()
    lfacts, extra = [], []
    for L in loaders:
        dec = html.unescape(L['src'])
        path, _, query = dec.partition('?')
        pairs = [p.split('=', 1) if '=' in p else [p, ''] for p in query.split('&')] if query else []
        params = {k: v for k, v in pairs}
        kind = {'action': 'branch', 'lazy': 'top_level'}.get(L['loading'], 'other')
        if kind == 'branch' and L['owner']:
            branch_owners.add(L['owner'])
        cursor = L['cursors'][0] if L['cursors'] else None
        lfacts.append({
            'kind': kind, 'id': L['id'], 'parent': L['owner'], 'parent_wire_depth': L['owner_depth'],
            'hint': L['numbers'][0] if L['numbers'] else None, 'faceplate_numbers': len(L['numbers']),
            'buttons': L['buttons'], 'hidden_cursor_inputs': len(L['cursors']), 'cursor': cursor,
            'path': path, 'src_decoded': dec,
            'src_entity_encoded': '&amp;' in L['src'] and '&' not in L['src'].replace('&amp;', ''),
            'startingDepth': _int(params.get('startingDepth')), 'sort': params.get('sort'),
            'seeker_session': params.get('seeker-session'),
        })
        extra.append({'params': [k for k, _ in pairs], 'method': L['method'], 'slot': L['slot'],
                      'other_inputs': L['inputs'] - len(L['cursors']),
                      'numbers_agree': len(set(L['numbers'])) <= 1, 'nouns': [n for n in L['nouns'] if n]})
    branch = [x for x in lfacts if x['kind'] == 'branch']
    top = [x for x in lfacts if x['kind'] == 'top_level']
    f['loaders'] = {
        'total': len(lfacts),
        'branch': len(branch),
        'top_level': len(top),
        'method_post': sum(1 for x in extra if x['method'] == 'post'),
        'slots': dict(sorted(collections.Counter(str(x['slot']) for x in extra).items())),
        'exactly_one_hidden_cursor_input': sum(
            1 for x, e in zip(lfacts, extra) if x['hidden_cursor_inputs'] == 1 and e['other_inputs'] == 0),
        'faceplate_numbers_agree': sum(1 for e in extra if e['numbers_agree']),
        'tracker_noun_counts': dict(sorted(collections.Counter(n for e in extra for n in e['nouns']).items())),
        'branch_with_button': sum(1 for x in branch if x['buttons'] > 0),
        'branch_hint_sum': sum(x['hint'] or 0 for x in branch),
        'branch_hints': [x['hint'] for x in branch],
        'faceplate_numbers_in_loaders': sum(x['faceplate_numbers'] for x in lfacts),
        'src_entity_encoded': sum(1 for x in lfacts if x['src_entity_encoded']),
        'with_sort_startingDepth_seeker_session': sum(
            1 for e in extra if {'sort', 'startingDepth', 'seeker-session'} <= set(e['params'])),
        'param_sets': dict(sorted(collections.Counter(','.join(e['params']) for e in extra).items())),
        'top_level_ids': [x['id'] for x in top],
        'top_level_params': [e['params'] for x, e in zip(lfacts, extra) if x['kind'] == 'top_level'],
        'items': lfacts,
    }
    afacts, own_links = [], []
    for a in anchors:
        noun = a['nouns'][0] if a['nouns'] else None
        href = html.unescape(a['href'] or '')
        own = bool(a['owner'] and a['owner'].startswith('t1_') and ('/comment/%s/' % a['owner'][3:]) in href)
        own_links.append(own)
        afacts.append({'parent': a['owner'], 'number': a['numbers'][0] if a['numbers'] else None,
                       'tracker_noun': noun, 'kind': 'loader_fallback' if a['owner'] in branch_owners else 'continue_thread'})
    f['permalink_anchors'] = {
        'total': len(afacts),
        'loader_fallback': sum(1 for a in afacts if a['kind'] == 'loader_fallback'),
        'continue_thread': sum(1 for a in afacts if a['kind'] == 'continue_thread'),
        'with_number': sum(1 for a in afacts if a['number'] is not None),
        'number_sum': sum(a['number'] or 0 for a in afacts),
        'tracker_noun_counts': dict(sorted(collections.Counter(a['tracker_noun'] for a in afacts if a['tracker_noun']).items())),
        'points_at_own_parent': sum(own_links),
        'items': afacts,
    }
    p = s['partials']
    f['partials'] = {'challenge_prompt': p['challenge-prompt'], 'human_verification_dialog': p['human-verification-dialog'],
                     'banned_user_banner': p['banned-user-banner'], 'private': p['private'], 'other': p['other']}
    f['markers'] = {'prove_your_humanity_text': s['visible'].count('prove your humanity'),
                    'over18': s['over18'], 'quarantine_text': s['quarantine']}
    if f['markers']['prove_your_humanity_text']:
        klass = 'challenge'
    elif (s['over18'] or s['quarantine']) and not comments:
        klass = 'gated'
    elif comments or s['post'] is not None:
        klass = 'ok'
    else:
        klass = 'unknown'
    f['page_class'] = klass
    return f


REDDIT_PRESERVED = [
    'comments', 'unique_thing_ids', 'thingId_attrs_camelcase', 'thingid_attrs_lowercase', 'with_parentId',
    'top_level', 'top_level_with_t3_postId', 'wire_depth_histogram', 'nesting_parent_equals_parentId',
    'derived_depth_mismatches', 'derived_depth_unresolved', 'deleted', 'comments_with_body', 'body_slots',
    'expected', 'page_class', 'markers']


def reddit_compare(raw_f, fx_f):
    """Counts that sanitization must preserve. Returns a list of mismatching keys."""
    bad = [k for k in REDDIT_PRESERVED if raw_f[k] != fx_f[k]]
    rl, fl = raw_f['loaders'], fx_f['loaders']
    for k in ('total', 'branch', 'top_level', 'method_post', 'exactly_one_hidden_cursor_input', 'branch_with_button',
              'branch_hint_sum', 'branch_hints', 'faceplate_numbers_in_loaders', 'src_entity_encoded',
              'with_sort_startingDepth_seeker_session', 'top_level_params', 'param_sets', 'slots',
              'faceplate_numbers_agree', 'tracker_noun_counts'):
        if rl[k] != fl[k]:
            bad.append('loaders.' + k)
    for i, (a, b) in enumerate(zip(rl['items'], fl['items'])):
        for k in ('kind', 'hint', 'faceplate_numbers', 'buttons', 'startingDepth', 'sort', 'seeker_session',
                  'parent_wire_depth', 'hidden_cursor_inputs', 'src_entity_encoded'):
            if a[k] != b[k]:
                bad.append('loaders.items[%d].%s' % (i, k))
    ra, fa = raw_f['permalink_anchors'], fx_f['permalink_anchors']
    for k in ('total', 'loader_fallback', 'continue_thread', 'with_number', 'number_sum', 'tracker_noun_counts',
              'points_at_own_parent'):
        if ra[k] != fa[k]:
            bad.append('permalink_anchors.' + k)
    for k in ('challenge_prompt', 'human_verification_dialog', 'banned_user_banner'):
        if raw_f['partials'][k] != fx_f['partials'][k]:
            bad.append('partials.' + k)
    rr = [(r['wire_depth'], r['text_nonspace_chars']) for r in raw_f['records']]
    fr = [(r['wire_depth'], r['text_nonspace_chars']) for r in fx_f['records']]
    if rr != fr:
        bad.append('records.wire_depth/text_nonspace_chars')
    return bad


# -------------------------------------------------------- X facts (JSON walk)

X_KNOWN_ENTRY = frozenset(['TimelineTimelineItem', 'TimelineTimelineModule', 'TimelineTimelineCursor'])
X_KNOWN_ITEM = frozenset(['TimelineTweet', 'TimelineTimelineCursor'])
X_KNOWN_RESULT = frozenset(['Tweet', 'TweetWithVisibilityResults', 'TweetTombstone'])


def _x_unwrap(result):
    if not isinstance(result, dict):
        return None, None
    tn = result.get('__typename')
    if tn == 'TweetWithVisibilityResults':
        return tn, result.get('tweet')
    if tn in (None, 'Tweet'):
        return tn or 'Tweet', result
    return tn, None


def x_facts(obj):
    ins = x_instructions(obj)
    f = {'instructions': [i.get('type') for i in ins]}
    entries, terminates, add_items, replaced = [], [], [], []
    for i in ins:
        t = i.get('type')
        if t == 'TimelineAddEntries':
            entries.extend(i.get('entries') or [])
        elif t == 'TimelineTerminateTimeline':
            terminates.append(i.get('direction'))
        elif t == 'TimelineAddToModule':
            add_items.extend((i.get('moduleEntryId'), x) for x in i.get('moduleItems') or [])
        elif t == 'TimelineReplaceEntry':
            replaced.append(i)
    tweets, cursors, unknown_entries, unknown_items = [], [], [], []
    focal_entries = modules = module_items = 0

    def take_item(entry_id, ic, where):
        tn = ic.get('__typename') or ic.get('itemType')
        if tn == 'TimelineTweet':
            res = (ic.get('tweet_results') or {}).get('result')
            rtn, tw = _x_unwrap(res)
            tweets.append({'entry': entry_id, 'where': where, 'result_typename': rtn, 'tweet': tw,
                           'promoted': 'promotedMetadata' in ic or 'promoted-tweet' in (entry_id or ''),
                           'item_display': ic.get('tweetDisplayType')})
        elif tn == 'TimelineTimelineCursor':
            cursors.append({'entry': entry_id, 'where': where, 'type': ic.get('cursorType'),
                            'action_text': (ic.get('displayTreatment') or {}).get('actionText')})
        else:
            unknown_items.append(tn)

    for e in entries:
        c = e.get('content') or {}
        tn = c.get('__typename') or c.get('entryType')
        eid = e.get('entryId')
        if tn not in X_KNOWN_ENTRY:
            unknown_entries.append(tn)
        elif tn == 'TimelineTimelineItem':
            if (eid or '').startswith('tweet-'):
                focal_entries += 1
            take_item(eid, c.get('itemContent') or {}, 'entry')
        elif tn == 'TimelineTimelineModule':
            modules += 1
            for it in c.get('items') or []:
                module_items += 1
                take_item(it.get('entryId'), (it.get('item') or {}).get('itemContent') or {}, 'module')
        elif tn == 'TimelineTimelineCursor':
            cursors.append({'entry': eid, 'where': 'entry', 'type': c.get('cursorType'),
                            'action_text': (c.get('displayTreatment') or {}).get('actionText')})
    for mod_id, it in add_items:
        take_item(it.get('entryId'), (it.get('item') or {}).get('itemContent') or {}, 'add_to_module')
    for r in replaced:
        c = (r.get('entry') or {}).get('content') or {}
        if (c.get('__typename') or c.get('entryType')) == 'TimelineTimelineCursor':
            cursors.append({'entry': (r.get('entry') or {}).get('entryId'), 'where': 'replace_entry',
                            'type': c.get('cursorType'), 'action_text': None})

    focal = None
    for t in tweets:
        if t['where'] == 'entry' and (t['entry'] or '').startswith('tweet-') and t['tweet']:
            focal = t['tweet']
            break
    focal_id = focal.get('rest_id') if focal else None

    def handle(tw):
        u = ((tw.get('core') or {}).get('user_results') or {}).get('result') or {}
        return (u.get('core') or {}).get('screen_name')

    paths = collections.Counter()
    note_keys = quoted_keys = 0
    for path, k, v, parent in json_walk(obj):
        if k == 'screen_name':
            paths['.'.join(str(p) for p in path if not isinstance(p, int)).split('tweet_results.result.')[-1]] += 1
        elif k == 'note_tweet':
            note_keys += 1
        elif k == 'quoted_status_result':
            quoted_keys += 1
    tweet_list = []
    for t in tweets:
        tw = t['tweet'] or {}
        L = tw.get('legacy') or {}
        tweet_list.append({
            'entry': t['entry'], 'where': t['where'], 'result_typename': t['result_typename'],
            'id': tw.get('rest_id'), 'author': handle(tw) if tw else None,
            'in_reply_to': L.get('in_reply_to_status_id_str'), 'promoted': t['promoted'],
            'reply_count': L.get('reply_count'), 'note_tweet': 'note_tweet' in tw,
            'quoted': ((tw.get('quoted_status_result') or {}).get('result') or {}).get('rest_id'),
            'full_text_len': len(L['full_text']) if isinstance(L.get('full_text'), str) else None,
            'display_text_range': L.get('display_text_range'),
        })
    organic = [t for t in tweet_list if not t['promoted'] and t['result_typename'] != 'TweetTombstone']
    n_entries = len(entries)
    f.update({
        'entries': n_entries,
        'focal_tweet_entries': focal_entries,
        'modules': modules,
        'module_items': module_items,
        'cursor_entries': sum(1 for c in cursors if c['where'] == 'entry'),
        'unknown_entry_typenames': len(unknown_entries),
        'unknown_entry_typename_values': sorted(set(x for x in unknown_entries if x)),
        'unknown_entry_ratio': round(len(unknown_entries) / n_entries, 4) if n_entries else 0.0,
        'unknown_item_typenames': len(unknown_items),
        'timeline_tweet_items': len(tweets),
        'promoted': sum(1 for t in tweet_list if t['promoted']),
        'tombstones': sum(1 for t in tweet_list if t['result_typename'] == 'TweetTombstone'),
        'visibility_wrapped': sum(1 for t in tweet_list if t['result_typename'] == 'TweetWithVisibilityResults'),
        'result_typenames': dict(sorted(collections.Counter(t['result_typename'] for t in tweet_list).items())),
        'module_tweets_non_promoted': sum(1 for t in tweet_list if t['where'] == 'module' and not t['promoted']),
        'added_to_module_items': len(add_items),
        'replaced_entries': [ (r.get('entry_id_to_replace')) for r in replaced],
        'note_tweet_keys': note_keys,
        'quoted_status_result_keys': quoted_keys,
        'cursor_types': dict(sorted(collections.Counter(c['type'] for c in cursors).items())),
        'cursors': cursors,
        'terminate_directions': terminates,
        'screen_name_key_paths': dict(sorted(paths.items())),
        'legacy_screen_name_keys': sum(c for p, c in paths.items() if p.endswith('legacy')),
        'focal': {'id': focal_id, 'author': handle(focal) if focal else None,
                  'reply_count': (focal.get('legacy') or {}).get('reply_count') if focal else None,
                  'note_tweet': 'note_tweet' in focal if focal else None},
        'direct_replies_to_focal': sum(1 for t in organic if focal_id and t['in_reply_to'] == focal_id),
        'organic_tweets': len(organic),
        'tweets': tweet_list,
    })
    return f


X_PRESERVED = [
    'instructions', 'entries', 'focal_tweet_entries', 'modules', 'module_items', 'cursor_entries',
    'unknown_entry_typenames', 'unknown_item_typenames', 'timeline_tweet_items', 'promoted', 'tombstones',
    'visibility_wrapped', 'result_typenames', 'module_tweets_non_promoted', 'note_tweet_keys',
    'quoted_status_result_keys', 'cursor_types', 'terminate_directions', 'direct_replies_to_focal',
    'organic_tweets']


def x_compare(raw_f, fx_f):
    bad = [k for k in X_PRESERVED if raw_f[k] != fx_f[k]]
    if raw_f['focal']['reply_count'] != fx_f['focal']['reply_count']:
        bad.append('focal.reply_count')
    rt = [(t['where'], t['promoted'], t['reply_count'], t['note_tweet'], t['result_typename']) for t in raw_f['tweets']]
    ft = [(t['where'], t['promoted'], t['reply_count'], t['note_tweet'], t['result_typename']) for t in fx_f['tweets']]
    if rt != ft:
        bad.append('tweets')
    return bad


# --------------------------------------------------------------- facts.json

def fixture_files(out_dir):
    names = sorted(n for n in os.listdir(out_dir)
                   if os.path.isfile(os.path.join(out_dir, n)) and n.endswith(('.html', '.json')) and n != OUT_FACTS)
    return names


def read_text(path):
    with open(path, 'rb') as fh:
        return fh.read().decode('utf-8')


def compute_facts(out_dir):
    facts = {'_about': (
        'Expected facts per fixture, computed by scripts/sanitize_records_fixtures.py from the fixture files '
        'themselves with a regex tag scan (HTML) or a JSON walk (X), independently of the sanitizer\'s '
        'transformation code. Decoder tests assert against these values.')}
    for name in fixture_files(out_dir):
        path = os.path.join(out_dir, name)
        text = read_text(path)
        if name.endswith('.html'):
            f = reddit_facts(text)
        else:
            f = x_facts(json.loads(text))
        f['bytes'] = len(text.encode('utf-8'))
        if name in SYNTHETIC_EXPECT:
            f['expect'] = SYNTHETIC_EXPECT[name]
        facts[name] = f
    # cross-fixture: shared comment ids between the two sorts
    if OUT_REDDIT_SSR in facts and OUT_REDDIT_NEW in facts:
        a = {r['id'] for r in facts[OUT_REDDIT_SSR]['records']}
        b = {r['id'] for r in facts[OUT_REDDIT_NEW]['records']}
        facts['_cross'] = {'reddit_sorts_shared_ids': len(a & b), 'reddit_sorts_union_ids': len(a | b)}
    for name, (base, cursor) in FRAGMENT_ANSWERS.items():
        if name in facts and base in facts:
            loader = next((x for x in facts[base]['loaders']['items'] if x['cursor'] == cursor), None)
            if loader is None:
                raise SystemExit('fragment link: no loader with %s in %s' % (cursor, base))
            frag = facts[name]
            base_ids = {r['id']: r for r in facts[base]['records']}
            start = loader['startingDepth']
            parent_depth = base_ids[loader['parent']]['wire_depth'] if loader['parent'] in base_ids else None
            by_id = {r['id']: r for r in frag['records']}

            def absolute(rid, seen=()):
                r = by_id[rid]
                if r['parent'] == loader['parent']:
                    return None if parent_depth is None else parent_depth + 1
                if r['parent'] in by_id and r['parent'] not in seen:
                    d = absolute(r['parent'], seen + (rid,))
                    return None if d is None else d + 1
                return None

            derived = {rid: absolute(rid) for rid in by_id}
            frag['answers_loader'] = {
                'fixture': base, 'cursor': cursor, 'path': loader['path'], 'parent': loader['parent'],
                'parent_wire_depth': parent_depth, 'startingDepth': start, 'hint': loader['hint']}
            frag['derived_depth'] = derived
            frag['wire_offset'] = {rid: (derived[rid] - by_id[rid]['wire_depth'])
                                   if derived[rid] is not None and by_id[rid]['wire_depth'] is not None else None
                                   for rid in by_id}
            frag['all_parents_resolved_after_merge'] = all(
                r['parent'] in by_id or r['parent'] in base_ids for r in frag['records'])
            frag['ids_already_in_base'] = sorted(set(by_id) & set(base_ids))
            nested_hints = sum(x['hint'] or 0 for x in frag['loaders']['items'] if x['kind'] == 'branch')
            live = sum(1 for r in frag['records'] if r['author'] not in ('[deleted]', '[removed]'))
            hint = loader['hint'] or 0
            frag['hint_conservation'] = {
                'hint': hint, 'records_unique': len(by_id), 'records_not_deleted': live,
                'nested_hints': nested_hints, 'threshold': hint - max(2, -(-hint // 10)),
                'resolved_if_counting_all': len(by_id) + nested_hints >= hint - max(2, -(-hint // 10)),
                'resolved_if_excluding_deleted': live + nested_hints >= hint - max(2, -(-hint // 10))}
    return facts


def dump_facts(obj, depth=0):
    """Indented JSON with sorted keys; a list of objects is written one object per line."""
    pad = '  ' * depth
    if isinstance(obj, dict):
        if not obj:
            return '{}'
        rows = ['%s  %s: %s' % (pad, json.dumps(k), dump_facts(obj[k], depth + 1)) for k in sorted(obj)]
        return '{\n' + ',\n'.join(rows) + '\n' + pad + '}'
    if isinstance(obj, list) and any(isinstance(x, (dict, list)) for x in obj):
        rows = [pad + '  ' + json.dumps(x, ensure_ascii=False, sort_keys=True) for x in obj]
        return '[\n' + ',\n'.join(rows) + '\n' + pad + ']'
    return json.dumps(obj, ensure_ascii=False, sort_keys=True)


LOADER_COLUMNS = ['cursor', 'kind', 'parent', 'parent_wire_depth', 'hint', 'startingDepth', 'query']


def slim_facts(facts):
    """The written form: aggregates plus compact rows. Per-record and per-tweet rows stay
    internal (they drive the raw-versus-fixture cross-check) except for small fixtures."""
    out = {}
    for name, f in facts.items():
        if not isinstance(f, dict) or name.startswith('_'):
            out[name] = f
            continue
        g = dict(f)
        if 'records' in g:
            recs = g.pop('records')
            g['text_nonspace_chars_total'] = sum(r['text_nonspace_chars'] for r in recs)
            if len(recs) <= 10:
                g['records'] = recs
        if 'loaders' in g:
            L = dict(g['loaders'])
            L['paths'] = sorted({x['path'] for x in L['items']})
            L['item_columns'] = LOADER_COLUMNS
            L['items'] = [[x['cursor'], x['kind'], x['parent'], x['parent_wire_depth'], x['hint'], x['startingDepth'],
                           x['src_decoded'].partition('?')[2]] for x in L['items']]
            g['loaders'] = L
        if 'permalink_anchors' in g:
            A = dict(g['permalink_anchors'])
            items = A.pop('items')
            A['loader_fallback_parents'] = [a['parent'] for a in items if a['kind'] == 'loader_fallback']
            A['continue_thread_parents'] = [a['parent'] for a in items if a['kind'] == 'continue_thread']
            g['permalink_anchors'] = A
        if 'tweets' in g:
            tw = g.pop('tweets')
            g['promoted_entries'] = [t['entry'] for t in tw if t['promoted']]
            g['tombstone_entries'] = [t['entry'] for t in tw if t['result_typename'] == 'TweetTombstone']
            g['visibility_wrapped_ids'] = [t['id'] for t in tw if t['result_typename'] == 'TweetWithVisibilityResults']
            g['quoted_ids'] = [t['quoted'] for t in tw if t['quoted']]
            g['added_to_module_ids'] = [t['id'] for t in tw if t['where'] == 'add_to_module']
        out[name] = g
    return out


def write_facts(out_dir):
    facts = compute_facts(out_dir)
    path = os.path.join(out_dir, OUT_FACTS)
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(dump_facts(slim_facts(facts)) + '\n')
    return facts


# ------------------------------------------------------------------ privacy

_TOKEN_ATTR = re.compile(r'nonce|csrf|token|session|loid|correlation|render-?id|clienthash|impression|site-?key|'
                         r'signature|tracking|spp|ctn|platform-health|device', re.I)
_OPAQUE = re.compile(r'[A-Za-z0-9_+-]{20,}')
_NAME_TOKEN = re.compile(r'[A-Za-z0-9_-]+')
_TRIVIAL = frozenset(['true', 'false', 'null', 'none', '', '0', '1'])
WINDOW = 30


def _opaque_tokens(s):
    return {t for t in _OPAQUE.findall(s) if re.search(r'[A-Za-z]', t) and re.search(r'\d', t)}


def _norm_ws(s):
    return re.sub(r'\s+', ' ', s)


def _windows(s):
    s = _norm_ws(s).strip()
    out = set()
    for i in range(0, max(len(s) - WINDOW + 1, 0)):
        w = s[i:i + WINDOW]
        if sum(ch.isalpha() for ch in w) >= 8:
            out.add(w)
    return out


def collect_forbidden(reddit_paths, x_paths):
    F = collections.defaultdict(set)
    trivial_session = 0
    for path in reddit_paths:
        text = read_text(path)
        root = parse_html(text)
        for node in iter_nodes(root):
            if node.kind == 'text':
                par = node.parent.tag if node.parent is not None else ''
                if par not in ('script', 'style'):
                    F['snippet'] |= _windows(html.unescape(node.raw))
                continue
            if node.kind != 'el':
                continue
            for k, v, _ in node.attrs:
                if v is None:
                    continue
                lk = k.lower()
                dv = html.unescape(v)
                if lk in ('author', 'author-name', 'authorname', 'username', 'user-name') or (
                        lk == 'display-name' and 'subreddit' not in node.tag and 'community' not in node.tag):
                    if dv not in ('[deleted]', '[removed]', ''):
                        F['name'].add(dv)
                if _TOKEN_ATTR.search(lk):
                    if dv.lower() in _TRIVIAL or len(dv) < 8:
                        trivial_session += 1
                    else:
                        F['token'].add(dv)
                if lk in ('title', 'alt', 'label', 'post-title', 'placeholder', 'content'):
                    F['snippet'] |= _windows(dv)
                for m in _USER_PATH.finditer(dv):
                    F['name'].add(m.group(2))
                for m in re.finditer(r'seeker-session=([^&"\s]*)', dv):
                    if m.group(1).lower() in _TRIVIAL:
                        trivial_session += 1
                    else:
                        F['token'].add(m.group(1))
            if node.tag == 'input' and node.get('value'):
                nm = (node.get('name') or '').lower()
                if nm == 'cursor':
                    F['cursor'].add(html.unescape(node.get('value')))
                elif node.get('value').lower() not in _TRIVIAL and len(node.get('value')) >= 8:
                    F['token'].add(html.unescape(node.get('value')))
        for m in re.finditer(r'(?<![A-Za-z0-9])t([1-6])_([a-z0-9]{3,})', text):
            F['thing_id'].add(m.group(0))
            if m.group(1) in ('1', '3') and len(m.group(2)) >= 5:
                F['bare_id'].add(m.group(2))
        for m in re.finditer(r'"(?:csrf_token|csrfToken|token|accessToken|access_token|session_id|loid)"\s*:\s*"([^"]{8,})"', text):
            F['token'].add(m.group(1))
        F['opaque'] |= _opaque_tokens(text)
        F['opaque'] |= _opaque_tokens(html.unescape(text))
    for path in x_paths:
        obj = json.loads(read_text(path))
        for p, k, v, parent in json_walk(obj):
            if isinstance(v, str):
                if k in ('screen_name', 'in_reply_to_screen_name'):
                    F['name'].add(v)
                elif k == 'name' and isinstance(parent, dict) and ('screen_name' in parent or 'core' in p[-1:]):
                    F['display_name'].add(v)
                if k in ('rest_id', 'id_str', 'user_id_str', 'conversation_id_str') or (isinstance(k, str) and k.endswith('_id_str')):
                    if v.isdigit():
                        F['numeric_id'].add(v)
                    else:
                        if len(v) >= 8:
                            F['token'].add(v)
                        F['numeric_id'] |= set(re.findall(r'\d{6,}', v))
                if k == 'sortIndex':
                    F['numeric_id'].add(v)
                if k in ('entryId', 'moduleEntryId', 'entry_id_to_replace'):
                    F['numeric_id'] |= set(re.findall(r'\d{6,}', v))
                    m = _PROMO_HEX.search(v)
                    if m:
                        F['token'].add(m.group(2))
                if k == 'id' and len(v) >= 8:
                    F['token'].add(v)
                if k == 'value' and isinstance(parent, dict) and 'cursorType' in parent:
                    F['cursor'].add(v)
                if k in ('impressionId', 'impressionString', 'controllerData', 'injectionType') and len(v) >= 8:
                    F['token'].add(v)
                if re.match(r'https?://[^/\s]+/\S{4,}', v):
                    F['url'].add(v)
                for m in _MENTION.finditer(v):
                    F['name'].add(m.group(1))
                F['opaque'] |= _opaque_tokens(v)
                F['snippet'] |= _windows(v)
            elif isinstance(v, list) and k in ('allTweetIds', 'edit_tweet_ids', 'tweet_ids_str'):
                F['numeric_id'] |= {x for x in v if isinstance(x, str)}
    # Names shorter than three characters cannot be matched without false hits.
    F['name'] = {n for n in F['name'] if len(n) >= 3}
    F['display_name'] = {n for n in F['display_name'] if len(n.strip()) >= 4}
    return F, trivial_session


def _fixture_views(name, text):
    """(full text haystack, values-only haystack) for one fixture file's text."""
    full = text + '\n' + html.unescape(text)
    if name.endswith('.json'):
        vals = [v for _, _, v, _ in json_walk(json.loads(text)) if isinstance(v, str)]
        decoded = '\n'.join(vals)
        return full + '\n' + decoded, decoded
    if name.endswith('.html'):
        vals = []
        for node in iter_nodes(parse_html(text)):
            if node.kind == 'text':
                vals.append(html.unescape(node.raw))
            elif node.kind == 'el':
                vals.extend(html.unescape(v) for _, v, _ in node.attrs if v is not None)
        return full, '\n'.join(vals)
    return full, full


CATEGORIES = ['name', 'display_name', 'thing_id', 'bare_id', 'numeric_id', 'cursor', 'token', 'url', 'opaque',
              'snippet']


def scan_texts(F, items):
    """Count forbidden values found in (name, text) items. Returns a Counter per category."""
    leaks = collections.Counter()
    num_short = {x for x in F['numeric_id'] if len(x) < 6}
    num_long = F['numeric_id'] - num_short
    for name, text in items:
        full, values = _fixture_views(name, text)
        lower_tokens = {t.lower() for t in _NAME_TOKEN.findall(values)}
        bare_tokens = set(re.findall(r'[a-z0-9]+', full))
        digit_tokens = set(re.findall(r'\d+', full))
        values_lower = _norm_ws(values.lower())
        leaks['thing_id'] += sum(1 for x in F['thing_id'] if x in full)
        leaks['bare_id'] += len(F['bare_id'] & bare_tokens)
        leaks['name'] += sum(1 for n in F['name'] if n.lower() in lower_tokens)
        for n in F['display_name']:
            if re.search(r'(?<![a-z0-9])%s(?![a-z0-9])' % re.escape(_norm_ws(n.lower()).strip()), values_lower):
                leaks['display_name'] += 1
        leaks['numeric_id'] += len(num_long & digit_tokens)
        if name.endswith('.json') and num_short:
            id_values = set()
            for _, k, v, _ in json_walk(json.loads(text)):
                if isinstance(k, str) and (k in ('rest_id', 'id_str') or k.endswith('_id_str')) and isinstance(v, str):
                    id_values.add(v)
            leaks['numeric_id'] += len(num_short & id_values)
        for cat in ('cursor', 'token', 'url'):
            leaks[cat] += sum(1 for x in F[cat] if len(x) >= 8 and x in full)
        heads = set()
        for r in _OPAQUE.findall(full):
            for i in range(0, len(r) - 20 + 1):
                heads.add(r[i:i + 20])
        leaks['opaque'] += sum(1 for t in F['opaque'] if t[:20] in heads and t in full)
        norm_full = _norm_ws(full)
        hay = {hash(norm_full[i:i + WINDOW]) for i in range(0, max(len(norm_full) - WINDOW + 1, 0))}
        leaks['snippet'] += sum(1 for w in F['snippet'] if hash(w) in hay and w in norm_full)
    return leaks


def _scanned_files(out_dir):
    files = [os.path.join(out_dir, n) for n in sorted(os.listdir(out_dir)) if os.path.isfile(os.path.join(out_dir, n))]
    return files + [os.path.abspath(__file__)]


def verify(reddit_paths, x_paths, out_dir):
    F, trivial = collect_forbidden(reddit_paths, x_paths)
    files = _scanned_files(out_dir)
    leaks = scan_texts(F, [(os.path.basename(p), read_text(p)) for p in files])
    print('verify: forbidden values extracted from %d raw files: %s; trivial session/token values skipped: %d' % (
        len(reddit_paths) + len(x_paths), ', '.join('%s=%d' % (c, len(F[c])) for c in CATEGORIES), trivial))
    print('verify: scanned %d files (%d bytes) in %s plus this script' % (
        len(files) - 1, sum(os.path.getsize(p) for p in files[:-1]), out_dir))
    print('verify: leaks: %s; total=%d' % (', '.join('%s=%d' % (c, leaks[c]) for c in CATEGORIES), sum(leaks.values())))
    return sum(leaks.values())


def selftest(reddit_paths, x_paths, out_dir):
    """Negative control, in memory only: plant one raw value per category into a copy of
    each fixture's text and require the scan to report it. Nothing is written or printed
    except counts."""
    F, _ = collect_forbidden(reddit_paths, x_paths)
    texts = [(os.path.basename(p), read_text(p)) for p in _scanned_files(out_dir)]
    html_fx = next(t for t in texts if t[0] == OUT_REDDIT_SSR)
    json_fx = next(t for t in texts if t[0] == OUT_X)
    failures = 0
    for cat in CATEGORIES:
        pool = sorted(F[cat], key=lambda x: (-len(x), x)) if cat != 'name' else sorted(F[cat])
        pool = [x for x in pool if len(x) >= (8 if cat in ('cursor', 'token', 'url') else 1)]
        if not pool:
            print('selftest: %s: no forbidden values to plant' % cat)
            failures += 1
            continue
        value = pool[len(pool) // 2]
        if cat == 'numeric_id':
            value = max((x for x in F[cat] if x.isdigit()), key=len)
        if cat in ('numeric_id', 'display_name') or (cat == 'name' and value in {
                str(v) for _, _, v, _ in json_walk([json.loads(read_text(p)) for p in x_paths])}):
            name, text = json_fx
            obj = json.loads(text)
            ins = x_instructions(obj)
            planted_obj = {'data': {'planted': [value, ins[0].get('type')]}}
            planted = (name, json.dumps(obj)[:-1] + ',"planted":%s}' % json.dumps(planted_obj))
        else:
            name, text = html_fx
            planted = (name, text.replace('</body>', '<p title="%s">%s</p></body>' % (
                html.escape(value), html.escape(value)), 1))
        base = scan_texts(F, [t for t in texts if t[0] == planted[0]])[cat]
        hit = scan_texts(F, [planted])[cat]
        ok = hit > base
        failures += 0 if ok else 1
        print('selftest: %-12s planted 1 value (len=%d): baseline=%d after=%d %s' % (
            cat, len(value), base, hit, 'ok' if ok else 'NOT DETECTED'))
    return failures


# --------------------------------------------------------------------- main

def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()


def generate(args):
    os.makedirs(args.out, exist_ok=True)
    rs = RedditSanitizer()
    outputs = {}
    raw_facts = {}
    for src, name in ((args.reddit_ssr, OUT_REDDIT_SSR), (args.reddit_new, OUT_REDDIT_NEW)):
        raw = read_text(src)
        outputs[name] = rs.sanitize(raw)
        raw_facts[name] = reddit_facts(raw)
    xs = XSanitizer()
    raw_x = json.loads(read_text(args.x_tweetdetail))
    outputs[OUT_X] = json.dumps(xs.sanitize(raw_x), ensure_ascii=False, separators=(',', ':'))
    raw_facts[OUT_X] = x_facts(raw_x)
    for name, text in outputs.items():
        with open(os.path.join(args.out, name), 'w', encoding='utf-8') as fh:
            fh.write(text)
    facts = write_facts(args.out)
    problems = 0
    for name in (OUT_REDDIT_SSR, OUT_REDDIT_NEW):
        bad = reddit_compare(raw_facts[name], facts[name])
        log('cross-check %s: %s' % (name, 'ok' if not bad else 'MISMATCH ' + ', '.join(bad)))
        problems += len(bad)
    bad = x_compare(raw_facts[OUT_X], facts[OUT_X])
    log('cross-check %s: %s' % (OUT_X, 'ok' if not bad else 'MISMATCH ' + ', '.join(bad)))
    problems += len(bad)
    log('reddit: authors=%d ids=%d cursors=%d slugs=%d stats=%s' % (
        len(rs.authors), len(rs.ids), len(rs.cursors), len(rs.slugs), dict(sorted(rs.stats.items()))))
    log('x: tweets=%d users=%d handles=%d notes=%d stats=%s' % (
        len(xs.tweets), len(xs.users), len(xs.handles), len(xs.notes), dict(sorted(xs.stats.items()))))
    for name in outputs:
        log('wrote %s (%d bytes)' % (name, os.path.getsize(os.path.join(args.out, name))))
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('--reddit-ssr', help='raw Reddit thread document (default sort)')
    ap.add_argument('--reddit-new', help='raw Reddit thread document (sort=new)')
    ap.add_argument('--x-tweetdetail', help='raw X TweetDetail GraphQL response')
    ap.add_argument('--x-extra', action='append', default=[],
                    help='other raw X captures (JSON); used only as extra sources of forbidden values')
    ap.add_argument('--out', required=True, help='fixture directory (internal/records/testdata)')
    ap.add_argument('--verify', action='store_true', help='only check that no private value reaches any fixture')
    ap.add_argument('--facts-only', action='store_true', help='only recompute facts.json from the fixtures')
    ap.add_argument('--selftest', action='store_true',
                    help='with the raw inputs: plant one forbidden value per category in memory and require detection')
    ap.add_argument('--print-sources', action='store_true', help='print raw input sizes and SHA-256 (no content)')
    args = ap.parse_args(argv)
    raws_reddit = [p for p in (args.reddit_ssr, args.reddit_new) if p]
    raws_x = [p for p in [args.x_tweetdetail] + args.x_extra if p]
    if args.print_sources:
        for p in raws_reddit + raws_x:
            print('%s %d %s' % (os.path.basename(p), os.path.getsize(p), sha256(p)))
        return 0
    if args.facts_only:
        write_facts(args.out)
        log('wrote %s' % os.path.join(args.out, OUT_FACTS))
        return 0
    if args.selftest:
        if not (raws_reddit and raws_x):
            ap.error('--selftest needs the Reddit and X raw inputs')
        return 1 if selftest(raws_reddit, raws_x, args.out) else 0
    if args.verify:
        if not raws_reddit and not raws_x:
            ap.error('--verify needs the raw inputs')
        return 1 if verify(raws_reddit, raws_x, args.out) else 0
    if not (args.reddit_ssr and args.reddit_new and args.x_tweetdetail):
        ap.error('generation needs --reddit-ssr, --reddit-new and --x-tweetdetail')
    problems = generate(args)
    leaks = verify(raws_reddit, raws_x, args.out)
    return 1 if problems or leaks else 0


if __name__ == '__main__':
    sys.exit(main())
