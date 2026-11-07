# Tiro 项目记忆

## 定位

Tiro 把飞书妙记自动整理成会议纪要：发现授权用户名下新生成的妙记，取回逐字稿与 AI 产物，交给大模型按模板写成纪要，在该用户的飞书云空间创建文档，再由机器人私聊把链接发给本人。

自托管的单体服务：Go 后端加 React 后台界面，前端构建产物嵌入二进制，单文件部署。只有出站请求，除后台端口外不需要对外开放任何入口。

## 技术栈

| 层 | 选型 |
| --- | --- |
| 语言 | Go 1.27 |
| HTTP | 标准库 `net/http` |
| 存储 | SQLite（`modernc.org/sqlite`，纯 Go 无 cgo），手写 SQL，不用 ORM |
| 日志 | 标准库 `log/slog` |
| 飞书 | 直接调用 HTTP 接口，不用官方 SDK |
| 前端 | React 19 + Vite + TypeScript + Tailwind CSS 4 + TanStack Query，工程在 `web/` |

## 约束

- 触发方式是定时搜索新妙记，不依赖事件
- 纪要生成走 OpenAI 格式的接口，地址、密钥、模型名在后台设置，密钥可以为空
- 纪要文档以授权人身份创建在其本人云空间的固定文件夹里，默认仅本人可见
- 妙记与云文档相关接口一律以用户身份调用，每个使用者扫码授权一次；机器人私聊用应用身份
- 全部配置存在数据目录的 SQLite 里，没有配置文件；启动参数只有监听地址与数据目录
- 飞书应用凭据、用户令牌、模型接口密钥以明文存在数据库里，数据目录不入库，接口只返回「是否已设置」
- 应用未配置时，创建应用的接口不需要登录；库里没有用户时，第一个扫码授权的人成为管理员
- 管理员统一配置飞书应用、模型接口与运行参数，管理成员，可查看全部成员的纪要，可手动添加妙记与重新生成；普通成员的妙记自动生成纪要，只能查看自己的纪要，看不到设置、成员列表与他人的纪要
- 登录只走飞书授权，授权即登录：浏览器里扫码，飞书客户端内打开时自动跳转网页授权；会话是签名 cookie，签名密钥首次启动时生成并存库

## 初始化与授权

后台首次打开进入三步向导：扫码创建飞书应用、扫码授权、填写模型接口。

- 创建应用：`POST https://accounts.feishu.cn/oauth/v1/app/registration`，表单 `action=begin` 发起、`action=poll` 轮询，成功后返回 `client_id` 与 `client_secret`；给用户扫的链接是 `https://open.feishu.cn/page/cli?user_code=…`
- 用户授权：`POST /oauth/v1/device_authorization` 发起，`POST /oauth/v3/token` 以 `grant_type=urn:ietf:params:oauth:grant-type:device_code` 轮询
- 令牌续期：`POST /oauth/v3/token`，JSON 请求体，`grant_type=refresh_token`。刷新令牌只能用一次，续期必须串行并立即落库；刷新令牌有效期 7 天，服务持续运行就不会过期
- 续期被拒绝时把用户标成授权失效并私聊通知，重新扫码登录后恢复
- 授权时申请的权限定义在 `internal/feishu/oauth.go` 的 `UserScopes`
- 飞书内免登：`GET https://accounts.feishu.cn/open-apis/authen/v1/authorize` 网页授权页，参数 `client_id`、`response_type=code`、`redirect_uri`、`scope`、`state`；回调 `/api/auth/feishu/callback` 后用 `POST /oauth/v3/token`，JSON 请求体，`grant_type=authorization_code` 换令牌，与扫码授权一样保存用户与令牌
- `redirect_uri` 按请求的主机与 `X-Forwarded-Proto`、`X-Forwarded-Host` 还原，必须先加到开发者后台「安全设置」的重定向 URL 里；授权失败时回到 `/?login_error=…`，登录页据此停止自动跳转

## 机器人菜单

- 菜单没有开放接口，只能在开发者后台「机器人」页的机器人自定义菜单里配置，响应动作选跳转至指定链接，发布版本后生效，仅单聊可见
- 菜单链接指向后台首页；桌面端可改用 `https://applink.feishu.cn/client/web_url/open?mode=sidebar-semi&url=…` 在聊天侧边栏打开
- 设置页按当前访问地址给出重定向 URL、菜单链接与桌面端链接

## 飞书接口

| 用途 | 接口 | 权限 |
| --- | --- | --- |
| 找新妙记 | `POST /open-apis/minutes/v1/minutes/search` | `minutes:minutes.search:read` |
| 妙记基本信息 | `GET /open-apis/minutes/v1/minutes/{minute_token}` | `minutes:minutes.basic:read` |
| 妙记内容 | `GET /open-apis/minutes/v1/minutes/{minute_token}/artifacts` | `minutes:minutes.artifacts:read` |
| 建文件夹 | `POST /open-apis/drive/v1/files/create_folder` | `space:folder:create` |
| 建文档 | `POST /open-apis/docs_ai/v1/documents` | `docx:document:create` |
| 机器人私聊 | `POST /open-apis/im/v1/messages` | 应用身份，`im:message:send_as_bot` |

- 搜索可按 `owner_ids` 与 `create_time.start_time` 过滤，`sorter` 用 `create_time_desc`；结果项只有 `token`、`display_info`、`meta_data`，标题、创建时间、时长要从基本信息接口取
- 新妙记生成约 6 分钟后才能被搜到；内容接口在妙记生成后立即可取
- `artifacts` 返回 `summary`、`keywords`、`transcript`，`minute_todos` 与 `minute_chapters` 不一定有，纪要以逐字稿为准
- 逐字稿每段是 `Speaker N 时间戳` 一行加正文一行，没有真人姓名
- 录音转写与外部设备上传的妙记都能搜到；上传的妙记 `create_time` 是上传时间
- `minutes.minute.generated_v1` 事件在开发者后台无法添加，订阅接口 `POST /open-apis/minutes/v1/minutes/subscription` 返回成功但事件不送达
- 建文件夹时 `folder_token` 传空串表示用户云空间根目录
- 建文档的请求体是 `format=markdown` 加 `content`，标题用 `<title>` 标签写在正文最前面；响应要么直接给出 `document`，要么给出异步任务，再轮询 `GET /open-apis/docs_ai/v1/async_tasks/{task_id}`
- Markdown 正文里可以混写飞书文档的 XML 标签，转成对应的原生块：`<callout>` 高亮块、`<grid>` 加 `<column>` 分栏、`<whiteboard>` 画板，语法见 larksuite/cli 仓库的 `skills/lark-doc/references/lark-doc-xml.md`。`- [ ]` 转成勾选框；单独成行的 `<checkbox>` 之间会多出空段落。链接后面的普通空格会被吞掉，要用不换行空格。半角 `<`、`$`、`*`、`_` 等字符会被当成语法，程序填写的纯文本要转义

## 处理流程

- 搜索起点是该用户上次检查时间再往前 24 小时，首次检查取最近 24 小时，最多回看 30 天，按 token 去重入库。录音类妙记的创建时间是开始录音的时间，录完才出现在搜索结果里，所以重叠要盖住最长的会议
- 有人完成授权或被重新启用时立即检查一次，不等检查间隔
- 用户首次授权之前创建的妙记只登记为已跳过，管理员可在后台手动生成
- 逐字稿为空时每 2 分钟再看一次，不计入尝试次数，入库 6 小时后仍为空则跳过；少于设定字数时跳过
- 一条记录最多自动尝试 3 次，间隔 1 分钟与 5 分钟；模型已产出正文、只是建文档失败时，重试复用正文不再调用模型
- 建文档的请求已被受理但没能确认结果时（轮询超时、进程退出），记录直接标为失败，不自动重试，避免在用户云空间留下重复文档
- 手动重新生成会清空上一次的正文与文档链接，重新调用模型并新建文档；正在生成中的记录不能重新排队
- 成员被停用只影响发现新妙记，已排队的记录照常处理
- 发起扫码的两个接口不需要登录，同时进行的扫码流程上限为 20 个
- 纪要生成后私聊只发一条文本消息，内容是文档链接，飞书客户端把它渲染成带正文预览的文档卡片；最终失败时发红色消息卡片

## 纪要模板

内置提示词在 `internal/summary/prompt.md`，可在后台改写。版式参照飞书自带的智能纪要。模型输出的第一行一级标题是会议主题，「参与人：」一行是参与者，这两行由程序取出：文档标题拼成「会议纪要：主题 2026年10月12日」，头部引用块写会议主题、起止时间与参与人。

正文的节标题都是一级标题，依次为：总结、关键决策、待办、风险与卡点、待确认、会议章节，末尾的「相关链接」由程序填写。

- 总结由三部分组成：灰色高亮块写核心结论，2 至 3 张并排的彩色卡片写议题概览，多级列表写讨论详情
- 关键决策按问题、讨论方案、决策依据展开，其余决策放在分割线之后
- 待办写成勾选框，写明负责人与具体截止日期，会上没说的标「待确认」
- 会议章节每章是「mm:ss 标题」一行加引用块摘要，程序把行首时间换成 `妙记链接?t=毫秒` 的跳转链接
- 专有名词按术语表纠正转写错误
- 同一件事只在最合适的一节展开，决策与待确认的条目后标出处时间
- 逻辑存疑或转写含糊的内容归入「待确认」
- 不写金句、反馈收集这类低信息量内容

## 开发约定

- Go 代码遵循 `go-style` skill，提交前 `make lint` 必须无 issue；lint 配置要求多于 5 个参数的调用逐行书写，同一作用域里不用 `:=` 重复接收 `err`
- 前端提交前 `pnpm lint` 与 `pnpm build` 必须无报错
- 前端构建产物输出到 `internal/web/dist`，不入库，只保留 `.gitkeep`
- 环境变量 `TIRO_FEISHU_OPEN_BASE` 与 `TIRO_FEISHU_ACCOUNTS_BASE` 可以把飞书请求指向本地的模拟服务，用于不依赖真实账号的联调；模拟服务是 `scripts/fake_feishu.py`，同时扮演飞书与模型接口，用法见文件头部
- 默认分支 `master`，提交信息遵循 `git-commit` skill
