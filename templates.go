package main

import (
	"bytes"
	"html/template"
	"log"
	"net/http"
	"net/url"
)

type PageData struct {
	Title    string
	LoggedIn bool
	Content  template.HTML
	Free     string
	Version  string
}

var pageTemplates = template.Must(template.New("root").Funcs(template.FuncMap{
	"q": url.QueryEscape,
}).Parse(baseTemplate + loginTemplate + galleryTemplate + uploadTemplate + viewTemplate))

func (a *App) renderPage(w http.ResponseWriter, r *http.Request, status int, title string, loggedIn bool, contentName string, data any) {
	var content bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&content, contentName, data); err != nil {
		log.Printf("render %s: %v", contentName, err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	free, _ := diskFree(a.root)
	page := PageData{
		Title:    title,
		LoggedIn: loggedIn,
		Content:  template.HTML(content.String()),
		Free:     freeText(free, a.root),
		Version:  version,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := pageTemplates.ExecuteTemplate(w, "base", page); err != nil {
		log.Printf("render base: %v", err)
	}
}

const baseTemplate = `{{define "base"}}<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0f1115">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
<title>{{.Title}} · 家庭相册</title>
<link rel="icon" href="data:,">
<link rel="stylesheet" href="/style.css">
</head>
<body>
{{if .LoggedIn}}
<header>
  <div class="wrap nav">
    <a class="brand" href="/gallery">家庭相册</a>
    <span class="free">{{.Free}}</span>
    <a class="btn ghost" href="/gallery">浏览</a>
    <a class="btn" href="/upload">上传</a>
    <form method="post" action="/logout"><button class="btn ghost" type="submit">退出</button></form>
  </div>
</header>
{{end}}
{{.Content}}
{{if .LoggedIn}}<footer>家庭相册 v{{.Version}} · 仅限局域网</footer>{{end}}
</body>
</html>{{end}}`

const loginTemplate = `{{define "login"}}
<div class="login-page">
  <div class="login-card">
    <div class="logo">📷</div>
    <h1>家庭相册</h1>
    <p class="sub">照片保存在客厅路由器的 USB 硬盘</p>
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    <form method="post" action="{{.NextAction}}">
      <label for="password">相册密码</label>
      <input id="password" name="password" type="password" autocomplete="current-password" autocapitalize="off" autofocus required>
      <button class="btn wide" type="submit">进入相册</button>
    </form>
    <div class="login-meta">{{.Free}}</div>
  </div>
</div>
{{end}}`

const galleryTemplate = `{{define "gallery"}}
<div class="wrap" id="gallery" data-path="{{.Rel}}">
  <div class="toolbar">
    <div class="headline">
      <h1>{{.Title}}</h1>
      <div class="sub">{{.Free}}{{if .HasItems}} · {{.Count}} 项{{end}}</div>
    </div>
    <div class="actions">
      <a class="btn" href="/upload">＋ 上传照片</a>
      <button class="btn secondary" type="button" onclick="createFolder()">新建文件夹</button>
    </div>
  </div>
  <nav class="breadcrumbs">
    {{range $i, $c := .Crumbs}}{{if $i}}<span class="sep">›</span>{{end}}<a href="{{$c.URL}}">{{$c.Name}}</a>{{end}}
  </nav>
  {{if .Dirs}}
  <div class="section-title">文件夹</div>
  <div class="grid">
    {{range .Dirs}}
    <a class="tile folder" href="/gallery?path={{.Rel}}">
      <div class="folder-icon">📁</div>
      <div class="folder-name">{{.Name}}</div>
      <div class="folder-meta">文件夹</div>
    </a>
    {{end}}
  </div>
  {{end}}
  {{if .Files}}
  <div class="section-title">照片和视频</div>
  <div class="grid">
    {{range .Files}}
      {{if eq .Kind "image"}}
      <a class="tile media" href="{{.ViewURL}}">
        <img src="{{.ThumbURL}}" data-thumb="{{.ThumbURL}}" loading="lazy" decoding="async" alt="{{.Name}}">
        <div class="file-meta"><span class="file-name">{{.Name}}</span><span class="file-size">{{.SizeText}}</span></div>
      </a>
      {{else if eq .Kind "video"}}
      <a class="tile video" href="{{.ViewURL}}">
        <div class="play-badge">▶</div>
        <div class="file-meta"><span class="file-name">{{.Name}}</span><span class="file-size">{{.SizeText}}</span></div>
      </a>
      {{else}}
      <a class="tile other" href="{{.Download}}">
        <div class="file-icon">📄</div>
        <div class="file-meta"><span class="file-name">{{.Name}}</span><span class="file-size">{{.SizeText}}</span></div>
      </a>
      {{end}}
    {{end}}
  </div>
  {{end}}
  {{if not .HasItems}}
  <div class="empty">
    <div class="empty-icon">🖼️</div>
    <h2>这里还没有照片</h2>
    <p>点“上传照片”，从 iPhone 相册选择照片或视频。</p>
    <a class="btn" href="/upload">去上传</a>
  </div>
  {{end}}
  {{if gt .Pages 1}}
  <div class="pager">
    {{if .PrevURL}}<a class="btn secondary" href="{{.PrevURL}}">上一页</a>{{else}}<span></span>{{end}}
    <span>第 {{.Page}} / {{.Pages}} 页</span>
    {{if .NextURL}}<a class="btn secondary" href="{{.NextURL}}">下一页</a>{{else}}<span></span>{{end}}
  </div>
  {{end}}
</div>
<script>
async function createFolder() {
  const parent = document.getElementById('gallery').dataset.path || '';
  const name = prompt('新文件夹名称');
  if (!name) return;
  const body = new URLSearchParams({path: parent, name: name});
  const response = await fetch('/mkdir', {
    method: 'POST',
    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
    body: body
  });
  if (response.ok) {
    window.location.href = response.url;
  } else {
    alert(await response.text());
  }
}
document.querySelectorAll('img[data-thumb]').forEach(function (img) {
  var tries = 0;
  function upgrade() {
    if (tries >= 3 || !img.naturalWidth || img.naturalWidth > 200) return;
    tries++;
    var url = img.dataset.thumb;
    setTimeout(function () {
      img.src = url + (url.indexOf('?') >= 0 ? '&' : '?') + 'r=' + tries;
    }, tries === 1 ? 8000 : 15000);
  }
  if (img.complete) upgrade(); else img.addEventListener('load', upgrade);
});
</script>
{{end}}`

const uploadTemplate = `{{define "upload"}}
<div class="wrap">
  <div class="toolbar">
    <div class="headline">
      <h1>上传照片</h1>
      <div class="sub">{{.Free}}</div>
    </div>
    <a class="btn secondary" href="/gallery">返回相册</a>
  </div>
  <form id="uploadForm" class="upload-card">
    <label>保存到</label>
    <div class="row">
      <select id="topFolder">
        <option value="iPhone备份">iPhone备份</option>
        <option value="共享">共享</option>
      </select>
      <input id="deviceName" type="text" placeholder="手机名称，例如：小鱼" value="iPhone" autocomplete="off" maxlength="40">
    </div>
    <div class="hint">每台 iPhone 填自己的名字，照片会保存到对应文件夹；下次会自动记住。</div>
    <div class="hint">系统会自动校验照片内容，相册里已经有的照片会跳过，不重复保存。</div>

    <label class="picker" for="files">
      <div class="picker-icon">＋</div>
      <div class="picker-title">选择照片或视频</div>
      <div class="picker-sub" id="fileSummary">支持多选，原图上传</div>
    </label>
    <input id="files" type="file" name="files" accept="image/*,video/*,.heic,.heif,.mov" multiple hidden>

    <button id="startButton" class="btn wide" type="submit" disabled>开始上传</button>
    <div id="progressBox" class="progress-box" hidden>
      <div class="bar"><div id="progressBar"></div></div>
      <div class="progress-text" id="progressText">准备上传…</div>
    </div>
    <div id="result" class="result"></div>
  </form>
</div>
<script>
(function () {
  const form = document.getElementById('uploadForm');
  const topFolder = document.getElementById('topFolder');
  const deviceName = document.getElementById('deviceName');
  const filesInput = document.getElementById('files');
  const fileSummary = document.getElementById('fileSummary');
  const startButton = document.getElementById('startButton');
  const progressBox = document.getElementById('progressBox');
  const progressBar = document.getElementById('progressBar');
  const progressText = document.getElementById('progressText');
  const result = document.getElementById('result');

  const savedName = localStorage.getItem('photosrv_device');
  if (savedName) deviceName.value = savedName;
  deviceName.addEventListener('input', function () {
    localStorage.setItem('photosrv_device', deviceName.value.trim());
  });

  function formatBytes(n) {
    if (n < 1024) return n + ' B';
    const units = ['KB', 'MB', 'GB', 'TB'];
    let value = n;
    for (let i = 0; i < units.length; i++) {
      value /= 1024;
      if (value < 1024) return value.toFixed(value >= 10 ? 1 : 2) + ' ' + units[i];
    }
    return value.toFixed(1) + ' PB';
  }

  function updateFileSummary() {
    const files = filesInput.files;
    if (!files.length) {
      fileSummary.textContent = '支持多选，原图上传';
      startButton.disabled = true;
      return;
    }
    let total = 0;
    for (let i = 0; i < files.length; i++) total += files[i].size;
    fileSummary.textContent = files.length + ' 个文件 · ' + formatBytes(total);
    startButton.disabled = false;
  }

  filesInput.addEventListener('change', updateFileSummary);

  form.addEventListener('submit', function (event) {
    event.preventDefault();
    const files = filesInput.files;
    if (!files.length) return;

    const name = (deviceName.value || '').replace(/[\/\\]/g, '-').trim() || '未命名手机';
    const folder = topFolder.value + '/' + name;
    const data = new FormData();
    for (let i = 0; i < files.length; i++) data.append('files', files[i], files[i].name);

    startButton.disabled = true;
    progressBox.hidden = false;
    result.textContent = '';
    progressBar.style.width = '0%';
    progressText.textContent = '准备上传…';

    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/upload?folder=' + encodeURIComponent(folder));
    xhr.responseType = 'json';
    xhr.upload.onprogress = function (e) {
      if (!e.lengthComputable) return;
      const percent = Math.round(e.loaded / e.total * 100);
      progressBar.style.width = percent + '%';
      progressText.textContent = '正在上传 ' + percent + '% · ' + formatBytes(e.loaded) + ' / ' + formatBytes(e.total);
    };
    xhr.onload = function () {
      startButton.disabled = false;
      const response = xhr.response || {};
      if (xhr.status === 200 && response.ok) {
        progressBar.style.width = '100%';
        progressText.textContent = '上传完成';
        const saved = response.saved || [];
        const duplicates = response.duplicates || [];
        const errors = response.errors || [];
        const parts = [];
        if (saved.length) parts.push('新增 ' + saved.length + ' 个文件');
        if (duplicates.length) parts.push('跳过 ' + duplicates.length + ' 个重复文件');
        if (errors.length) parts.push(errors.length + ' 个失败');
        result.textContent = parts.join('，') || '没有新增文件';
        if (duplicates.length) {
          const note = document.createElement('div');
          note.className = 'dup-note';
          note.textContent = '已存在：';
          duplicates.slice(0, 3).forEach(function (item, index) {
            if (index) note.appendChild(document.createTextNode('、'));
            const anchor = document.createElement('a');
            anchor.href = '/view?path=' + encodeURIComponent(item.existing);
            anchor.textContent = item.existing;
            note.appendChild(anchor);
          });
          if (duplicates.length > 3) note.appendChild(document.createTextNode(' 等'));
          result.appendChild(note);
        }
        const link = document.createElement('a');
        link.className = 'btn';
        link.href = (saved.length === 0 && response.duplicate_gallery) ? response.duplicate_gallery : response.gallery;
        link.textContent = '查看相册';
        result.appendChild(link);
        filesInput.value = '';
        updateFileSummary();
      } else {
        progressText.textContent = '上传失败';
        result.textContent = response.error || (xhr.status === 401 ? '登录已过期，请刷新页面重新登录。' : '上传失败，请重试。');
      }
    };
    xhr.onerror = function () {
      startButton.disabled = false;
      progressText.textContent = '网络错误';
      result.textContent = '上传失败，请检查 Wi-Fi 后重试。';
    };
    xhr.send(data);
  });
})();
</script>
{{end}}`

const viewTemplate = `{{define "view"}}
<div class="wrap">
  <div class="toolbar">
    <a class="btn secondary" href="{{.DirURL}}">← 返回</a>
    <div class="actions">
      {{if .PrevURL}}<a class="btn ghost" href="{{.PrevURL}}">上一张</a>{{end}}
      {{if .NextURL}}<a class="btn ghost" href="{{.NextURL}}">下一张</a>{{end}}
    </div>
  </div>
  <div class="viewer">
    <div class="viewer-head">
      <h1>{{.Name}}</h1>
      <div class="sub">{{.SizeText}} · {{.ModText}}</div>
    </div>
    {{if eq .Kind "image"}}
    <img class="full-media" src="{{.MediaURL}}" alt="{{.Name}}" onerror="this.hidden=true;document.getElementById('fallback').hidden=false;">
    {{else if eq .Kind "video"}}
    <video class="full-media" controls playsinline preload="metadata" src="{{.MediaURL}}" onerror="this.hidden=true;document.getElementById('videoFallback').hidden=false;"></video>
    <div id="videoFallback" class="fallback" hidden>
      当前浏览器不能播放这个视频编码（iPhone 的 HEVC/MOV 视频建议用 Safari 打开，或下载后播放）。<a class="btn" href="{{.DownloadURL}}">下载原文件</a>
    </div>
    {{else}}
    <div class="file-large">📄</div>
    {{end}}
    <div id="fallback" class="fallback" hidden>
      浏览器不能直接显示这个格式。<a class="btn" href="{{.DownloadURL}}">下载原文件</a>
    </div>
    <div class="viewer-actions">
      <a class="btn" href="{{.DownloadURL}}">下载原文件</a>
      <form method="post" action="/delete" onsubmit="return confirm('确定删除这张照片？删除后不可恢复。');">
        <input type="hidden" name="path" value="{{.Rel}}">
        <button class="btn danger" type="submit">删除</button>
      </form>
    </div>
  </div>
</div>
{{end}}`

const styleCSS = `
:root{
  --bg:#0f1115;
  --panel:#171a21;
  --card:#1c2029;
  --line:#2b313d;
  --text:#f3f5f8;
  --muted:#9aa4b4;
  --accent:#3b82f6;
  --accent-2:#2563eb;
  --danger:#ef4444;
  --ok:#22c55e;
}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{
  margin:0;
  background:var(--bg);
  color:var(--text);
  font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;
  line-height:1.45;
  padding-top:env(safe-area-inset-top);
}
a{color:inherit;text-decoration:none}
img{image-orientation:from-image}
button,input,select{font:inherit}
[hidden]{display:none!important}
.wrap{width:min(1120px,100%);margin:0 auto;padding:16px}
header{
  position:sticky;top:0;z-index:30;
  background:rgba(15,17,21,.9);
  border-bottom:1px solid var(--line);
  backdrop-filter:saturate(160%) blur(14px);
  -webkit-backdrop-filter:saturate(160%) blur(14px);
}
.nav{display:flex;align-items:center;gap:8px;flex-wrap:wrap;min-height:58px}
.brand{font-weight:750;font-size:18px;letter-spacing:.2px;margin-right:auto}
.free{color:var(--muted);font-size:13px;white-space:nowrap}
.btn{
  display:inline-flex;align-items:center;justify-content:center;gap:6px;
  min-height:40px;padding:0 14px;border-radius:10px;border:1px solid transparent;
  background:var(--accent);color:#fff;font-weight:650;font-size:15px;
  cursor:pointer;text-align:center;white-space:nowrap;
}
.btn:hover{background:var(--accent-2)}
.btn.secondary{background:#242a35;color:var(--text);border-color:var(--line)}
.btn.secondary:hover{background:#2d3441}
.btn.ghost{background:transparent;color:var(--text);border-color:var(--line)}
.btn.ghost:hover{background:#222833}
.btn.danger{background:#b91c1c}
.btn.danger:hover{background:#dc2626}
.btn.wide{width:100%;min-height:48px;font-size:16px}
.btn:disabled{opacity:.45;cursor:not-allowed}
main{padding-bottom:60px}
.toolbar{display:flex;align-items:center;gap:12px;justify-content:space-between;margin:8px 0 14px;flex-wrap:wrap}
.headline{min-width:0}
h1{font-size:25px;line-height:1.2;margin:0 0 4px;font-weight:750;letter-spacing:-.3px}
.sub{color:var(--muted);font-size:13px}
.actions{display:flex;gap:8px;flex-wrap:wrap}
.breadcrumbs{
  display:flex;align-items:center;gap:7px;flex-wrap:wrap;
  color:var(--muted);font-size:13px;margin:6px 0 18px;
}
.breadcrumbs a{color:#a9c9ff}
.breadcrumbs .sep{opacity:.5}
.section-title{font-size:15px;font-weight:700;color:#cdd4df;margin:20px 0 10px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(155px,1fr));gap:10px}
.tile{
  position:relative;display:block;background:var(--card);border:1px solid var(--line);
  border-radius:14px;overflow:hidden;min-height:140px;
  transition:transform .15s ease,border-color .15s ease;
}
.tile:hover{transform:translateY(-1px);border-color:#3c4657}
.tile.media img{display:block;width:100%;aspect-ratio:1/1;height:auto;object-fit:cover;background:#090b0e}
.file-meta{
  position:absolute;left:0;right:0;bottom:0;display:flex;flex-direction:column;gap:2px;
  padding:22px 9px 8px;background:linear-gradient(transparent,rgba(0,0,0,.86));
  font-size:12px;line-height:1.25;
}
.file-name{font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.file-size{color:#c2c9d5;font-size:11px}
.folder{
  display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;
  min-height:150px;background:linear-gradient(145deg,#202632,#171b22);padding:14px;text-align:center;
}
.folder-icon{font-size:44px;line-height:1}
.folder-name{font-weight:650;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.folder-meta{color:var(--muted);font-size:12px}
.video{
  background:linear-gradient(145deg,#263144,#151922);
  display:flex;align-items:center;justify-content:center;
}
.play-badge{
  width:56px;height:56px;border-radius:50%;background:rgba(255,255,255,.14);
  border:1px solid rgba(255,255,255,.28);display:flex;align-items:center;justify-content:center;
  font-size:22px;padding-left:4px;
}
.other{display:flex;align-items:center;justify-content:center;min-height:150px;background:#202632}
.file-icon{font-size:42px}
.empty{
  margin:48px auto;max-width:480px;text-align:center;background:var(--panel);
  border:1px solid var(--line);border-radius:18px;padding:32px 22px;
}
.empty-icon{font-size:50px}
.empty h2{margin:8px 0 6px;font-size:20px}
.empty p{color:var(--muted);margin:0 0 18px}
.pager{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:24px 0 6px;color:var(--muted);font-size:14px}
.login-page{min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
.login-card{
  width:min(390px,100%);background:var(--panel);border:1px solid var(--line);
  border-radius:22px;padding:28px;box-shadow:0 24px 70px rgba(0,0,0,.35);
}
.logo{font-size:42px;margin-bottom:8px}
.login-card h1{margin-bottom:6px}
.login-card .sub{margin:0 0 22px}
.login-card label{display:block;font-size:13px;color:var(--muted);margin-bottom:7px}
.login-card input{
  width:100%;height:48px;border-radius:11px;border:1px solid var(--line);
  background:#11151c;color:var(--text);padding:0 13px;margin-bottom:16px;font-size:17px;outline:none;
}
.login-card input:focus{border-color:#4c8df6}
.error{background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.4);color:#ffb4b4;padding:10px 12px;border-radius:10px;margin-bottom:14px;font-size:14px}
.login-meta{text-align:center;color:var(--muted);font-size:12px;margin-top:16px}
.upload-card{
  background:var(--panel);border:1px solid var(--line);border-radius:18px;padding:18px;
  max-width:720px;margin:0 auto;
}
.upload-card>label{display:block;font-size:13px;color:var(--muted);margin-bottom:8px}
.row{display:grid;grid-template-columns:140px 1fr;gap:10px}
select,input[type=text]{
  width:100%;height:46px;border-radius:11px;border:1px solid var(--line);
  background:#11151c;color:var(--text);padding:0 12px;font-size:16px;outline:none;
}
select:focus,input[type=text]:focus{border-color:#4c8df6}
.hint{color:var(--muted);font-size:12px;margin:7px 0 18px}
.picker{
  display:flex!important;flex-direction:column;align-items:center;justify-content:center;
  border:1.5px dashed #3b4556;border-radius:16px;background:#131821;
  min-height:190px;padding:24px;margin:0 0 16px!important;cursor:pointer;text-align:center;
}
.picker:hover{border-color:#5b8fe8;background:#151d29}
.picker-icon{font-size:40px;line-height:1;color:#83b4ff;margin-bottom:8px}
.picker-title{font-size:17px;font-weight:700}
.picker-sub{color:var(--muted);font-size:13px;margin-top:5px}
.progress-box{margin-top:16px;background:#121721;border:1px solid var(--line);border-radius:12px;padding:13px}
.bar{height:9px;background:#0b0e13;border-radius:99px;overflow:hidden}
#progressBar{height:100%;width:0;background:linear-gradient(90deg,#3b82f6,#60a5fa);transition:width .2s}
.progress-text{color:var(--muted);font-size:12px;margin-top:8px}
.result{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-top:14px;font-size:14px}
.dup-note{flex-basis:100%;color:var(--muted);font-size:12px;overflow-wrap:anywhere}
.dup-note a{color:#a9c9ff;text-decoration:underline;text-underline-offset:2px}
.viewer{background:#0b0d11;border:1px solid var(--line);border-radius:16px;padding:14px;margin-top:8px}
.viewer-head{display:flex;align-items:baseline;gap:10px;justify-content:space-between;flex-wrap:wrap;margin-bottom:12px}
.viewer-head h1{font-size:16px;margin:0;overflow-wrap:anywhere}
.full-media{display:block;width:100%;max-height:78vh;object-fit:contain;background:#000;border-radius:12px;margin:0 auto}
.viewer-actions{display:flex;justify-content:center;align-items:center;gap:10px;flex-wrap:wrap;margin-top:14px}
.viewer-actions form{display:flex}
.fallback{text-align:center;padding:28px 12px;color:var(--muted)}
.file-large{font-size:72px;text-align:center;padding:60px 0}
footer{text-align:center;color:#6d7788;font-size:11px;padding:24px 12px calc(24px + env(safe-area-inset-bottom))}
@media (max-width:560px){
  .wrap{padding:12px}
  .nav{min-height:54px}
  .brand{font-size:17px}
  .free{display:none}
  .nav .btn{min-height:36px;padding:0 10px;font-size:14px}
  h1{font-size:22px}
  .toolbar{align-items:flex-start}
  .grid{grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
  .tile{min-height:130px;border-radius:12px}
  .folder{min-height:140px}
  .folder-icon{font-size:38px}
  .row{grid-template-columns:1fr}
  .pager{font-size:12px}
  .pager .btn{padding:0 10px}
}
`
