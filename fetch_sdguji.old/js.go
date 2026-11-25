package main

// 说明：正文数据直接从浏览器收到的 SSR 响应体里解析（见 parse.go），
// 这里只保留页面发现、诊断和 DOM 兜底所需的脚本。

// exprFindStartChapter 在图书详情页上找目录中的第一章。
// 站点目录树是虚拟列表，但首项一定是全书第一章，取第一个即可。
const exprFindStartChapter = `(() => {
  const tree = document.querySelector('.semi-tree-option-list');
  let a = null;
  if (tree) {
    a = tree.querySelector('a[href*="/chapter/"]');
  }
  if (!a) {
    const all = [...document.querySelectorAll('a[href*="/chapter/"]')];
    a = all.find(x => (x.innerText || '').trim() !== '下一篇') || all[0];
  }
  if (!a) return null;
  const m = (a.getAttribute('href') || '').match(/chapter\/([^/?#]+)/);
  if (!m) return null;
  return {
    chapterId: m[1],
    title: (a.innerText || '').replace(/\s+/g, ' ').trim()
  };
})()`

// exprPageDiag 用于失败时输出页面状态，便于判断是风控、登录还是页面结构变化。
const exprPageDiag = `(() => {
  const body = (document.body && document.body.innerText || '').replace(/\s+/g, ' ').slice(0, 240);
  return {url: location.href, title: document.title || '', body: body};
})()`

// exprArticle 读取文本阅读区（DOM 兜底翻页时用）。
const exprArticle = `(() => {
  const main = document.querySelector('main.read-layout-main');
  if (!main) return null;
  const ps = [...main.querySelectorAll('p')].map(p => (p.innerText || '').replace(/\u00a0/g, ' ').trim()).filter(t => t);
  const curEl = document.querySelector('.prefix-container .prefix');
  const sufEl = document.querySelector('.suffix');
  const cur = curEl ? parseInt((curEl.innerText || '').trim(), 10) : 0;
  const total = sufEl ? parseInt((sufEl.innerText || '').replace('/', '').trim(), 10) : 0;
  return {ps: ps, cur: cur, total: total};
})()`
