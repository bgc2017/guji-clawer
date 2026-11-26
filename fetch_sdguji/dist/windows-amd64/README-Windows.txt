识典古籍下载器 · Windows 版
==============================

一、系统要求
------------
1. Windows 10 / 11，64 位。
2. 装有 Chrome 或 Edge（Win10/11 自带 Edge 即可，程序会自动探测）。
   程序通过真实浏览器访问 shidianguji.com，不依赖任何私有接口。
3. 无需安装 Go、Python 或其它运行库，单文件 exe 直接跑。

二、快速开始
------------
把 sdguji.exe 放到任意目录（路径尽量短，别放网盘同步目录），打开
cmd 或 PowerShell，cd 到该目录：

    sdguji.exe -book https://www.shidianguji.com/book/CADAL01023318

默认输出到当前目录下的 books\<图书ID>-<书名>\，每章一个 txt，
另有 book.json（进度清单）和 <书名>_全本.txt（全书合并）。

也可以直接双击 example.bat，按提示改图书链接。

三、常用参数
------------
  -book    图书 URL 或 ID（必填）
  -out     输出目录，默认 books；建议用短路径，如 D:\guji
  -list    只列目录，不下载
  -only    只抓指定章，标题或章节 ID，逗号分隔
  -from    从某章开始抓
  -max     最多抓几章
  -force   忽略已下载文件，强制重抓
  -text    正文字形：trad 标准繁体（默认）/ simp 机器简体 / orig 站点原文
  -visible 显示浏览器窗口（排查问题、过验证码时用）
  -delay   每章间隔毫秒，默认 800，抓整本别调太小
  -chrome  指定浏览器路径（自动探测失败时用）
  -ua      自定义 User-Agent
  -proxy   浏览器代理，如 http://127.0.0.1:7890
  -v       输出调试信息

四、常见问题
------------
1) 提示“未找到 Chrome 或 Edge”
   -chrome "C:\Program Files\Google\Chrome\Application\chrome.exe"
   或安装 Chrome 后重试。Edge 一般在：
   C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe

2) SmartScreen 提示“未知发布者”，或解压后双击 exe 没反应
   这是未签名的自编译程序：点“更多信息 → 仍要运行”。
   若从网上下载的 zip 被标记，右键 sdguji.exe → 属性 → 勾选“解除锁定”，
   或在 PowerShell 里执行：Unblock-File .\sdguji.exe
   若被公司杀软拦截，把 exe 加入白名单。

3) 控制台中文乱码
   程序启动时会自动把代码页切到 UTF-8；若仍有问题，先执行 chcp 65001。

4) 输出路径报错 / 文件名异常
   用 -out D:\guji 指定纯英文短路径；OneDrive 同步目录可能锁文件。

5) 中途断了怎么续
   直接重跑同一条命令，已完成的章会自动跳过；换过 -text 后
   只有字形不一致的章会重抓。

6) 抓整本太慢 / 被限流
   默认每章间隔约 0.8 秒，属正常节奏；遇到验证码用 -visible
   手动过一次，或换时段再跑。不要开多个进程同时抓同一站点。

五、注意
--------
- 正文来自站点 OCR 结果，-text trad/simp 只做字形归一，不做校勘。
- 只抓文字，不下载书影图片。
- 请遵守站点使用条款，控制请求频率，仅作个人研读用途。
