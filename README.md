# 局域网家庭相册

一个单二进制的局域网照片 / 视频相册，适合路由器、NAS、树莓派或家里的小主机。手机连上 Wi-Fi 后，就能浏览、上传和备份照片，也可以直接在线播放视频。

## 功能

- 密码登录，HMAC 签名 Cookie 会话
- 文件夹浏览、面包屑导航、分页
- 图片缩略图（优先读取 EXIF 内嵌缩略图，低配设备也很快）
- 视频在线播放，保留 iPhone 原始 MOV / HEVC 文件
- 拖拽或选择上传，支持大文件
- 新建文件夹、删除文件 / 文件夹
- SHA-256 内容索引，上传前判断是否已经存档
- 显示磁盘剩余空间
- 移动端适配，支持 iOS Safari
- 无数据库，索引为单个 JSON 文件

## 技术栈

- Go 1.26，标准库 `net/http`
- `golang.org/x/image` 图片缩放
- 原生 HTML 模板 + CSS，无前端构建步骤

## 构建

```bash
go build -o photosrv .
```

交叉编译到常见路由器（ARMv7）：

```bash
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o photosrv-arm .
```

## 运行

Linux / macOS：

```bash
export PHOTOSRV_PASSWORD='换成你的登录密码'
export PHOTOSRV_SECRET='至少 16 位的随机字符串'

./photosrv \
  -listen 0.0.0.0:8088 \
  -root /path/to/photos \
  -thumb-cache /path/to/thumb-cache
```

Windows PowerShell：

```powershell
$env:PHOTOSRV_PASSWORD = "换成你的登录密码"
$env:PHOTOSRV_SECRET   = "至少 16 位的随机字符串"

.\photosrv.exe -listen 0.0.0.0:8088 -root .\photos -thumb-cache .\thumbs
```

启动后访问 `http://<设备局域网 IP>:8088`。

生成随机密钥：

```bash
openssl rand -hex 32
```

## 参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8088` | 监听地址；局域网访问用 `0.0.0.0:8088` |
| `-root` | `./photos` | 照片 / 视频根目录 |
| `-thumb-cache` | `./thumbs` | 缩略图缓存目录 |
| `-index` | `<root 上级>/.photosrv-index.json` | 内容索引文件 |
| `-max-upload-gb` | `20` | 单文件上传上限（GiB） |
| `-cookie-hours` | `720` | 登录有效期（小时） |
| `-version` | | 打印版本号 |

## 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `PHOTOSRV_PASSWORD` | 是 | 相册登录密码 |
| `PHOTOSRV_SECRET` | 建议 | Cookie 签名密钥，至少 16 字节；缺失时会生成临时密钥，重启后登录失效 |

## 目录结构

运行时会用到类似下面的目录：

```text
photos/                     # -root，照片和视频
  2026/
    11/
      IMG_0001.jpeg
thumbs/                     # -thumb-cache，缩略图缓存
.photosrv-index.json        # 内容索引
```

`photos/`、`thumbs/` 和索引文件都已在 `.gitignore` 中排除。仓库只包含服务端代码，不含任何照片、视频或部署密码。

## 安全建议

- 默认只监听 `127.0.0.1`；确认需要局域网访问后再改成 `0.0.0.0`。
- 不要直接把这个服务暴露到公网。需要远程访问时，优先使用 Tailscale、WireGuard 等 VPN，或反向代理加 HTTPS。
- 密码和密钥只通过环境变量传入，不要写进代码、脚本或提交到仓库。
- 定期备份 `-root` 目录和索引文件。

## 说明

项目最初为家庭路由器上的 USB 硬盘设计，内存占用做了限制（64 MiB），因此在 ARM 路由器和低配 NAS 上也能运行。
