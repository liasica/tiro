# Tiro 项目记忆

## 定位

Tiro 把飞书妙记自动整理成会议纪要：发现授权用户名下新生成的妙记，取回逐字稿与 AI 产物，交给大模型按模板写成纪要，在该用户的飞书云空间创建文档，再由机器人私聊把链接发给本人。

成员还可以在后台或机器人私聊里与对话 Agent 问答，Agent 检索妙记与纪要作答并附引用来源；每条有结果的回答按类型（待办清单、会议总结等）整理成一篇飞书文档，存进提问人的纪要文件夹。

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

- 发现新妙记靠定时搜索，不依赖事件；机器人私聊消息经长连接接收，长连接也是出站连接
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
| 更新回答卡片 | `PATCH /open-apis/im/v1/messages/{message_id}` | 应用身份，`im:message:send_as_bot` |
| 长连接接入 | `POST /callback/ws/endpoint` | 应用凭据 |
| 接收私聊消息 | 事件 `im.message.receive_v1` | `im:message.p2p_msg:readonly` |

- 搜索可按 `owner_ids` 与 `create_time.start_time` 过滤，`sorter` 用 `create_time_desc`；结果项只有 `token`、`display_info`、`meta_data`，标题、创建时间、时长要从基本信息接口取
- 新妙记生成约 6 分钟后才能被搜到；内容接口在妙记生成后立即可取
- `artifacts` 返回 `summary`、`keywords`、`transcript`，`minute_todos` 与 `minute_chapters` 不一定有，纪要以逐字稿为准
- 逐字稿每段是 `Speaker N 时间戳` 一行加正文一行，没有真人姓名
- 录音转写与外部设备上传的妙记都能搜到；上传的妙记 `create_time` 是上传时间
- `minutes.minute.generated_v1` 事件在开发者后台无法添加，订阅接口 `POST /open-apis/minutes/v1/minutes/subscription` 返回成功但事件不送达
- 建文件夹时 `folder_token` 传空串表示用户云空间根目录
- 建文档的请求体是 `format=markdown` 加 `content`，标题用 `<title>` 标签写在正文最前面；响应要么直接给出 `document`，要么给出异步任务，再轮询 `GET /open-apis/docs_ai/v1/async_tasks/{task_id}`
- Markdown 正文里可以混写飞书文档的 XML 标签，转成对应的原生块：`<callout>` 高亮块、`<grid>` 加 `<column>` 分栏、`<whiteboard>` 画板，语法见 larksuite/cli 仓库的 `skills/lark-doc/references/lark-doc-xml.md`。`- [ ]` 转成勾选框；单独成行的 `<checkbox>` 之间会多出空段落。链接后面的普通空格会被吞掉，要用不换行空格。半角 `<`、`$`、`*`、`_` 等字符会被当成语法，程序填写的纯文本要转义

## 对话

- 对话 Agent 在 `internal/chat`，提示词是 `internal/chat/prompt.md`，网页与机器人共用。模型通过工具检索：`search_minutes` 按时间段与关键词找妙记，`read_minutes` 读纪要正文，`read_transcript` 用所有者的令牌读逐字稿；一个问题最多 8 轮工具调用，用完后要求直接作答
- 对话模型按接口配置：填接口地址与密钥后经 `GET {接口地址}/models` 取模型列表，勾选给对话用的模型并指定默认模型；接口 ID 由前端生成，模型 ID 是「接口 ID:模型名」；密钥留空时沿用同一地址已保存的密钥（这个接口原来的或纪要模型的），地址变了不沿用。一个接口都没有时沿用纪要的模型。网页提问时可选模型，机器人用默认模型。模型必须支持流式输出与工具调用
- 对话模型接口有 Chat Completions 与 Responses 两种格式，在接口上设置，没设置时 `api.openai.com` 用 Responses、其余用 Chat Completions。OpenAI 的新模型在 Chat Completions 里不能同时用推理与工具调用（报 `Function tools with reasoning_effort are not supported`），只能走 `/v1/responses`。Responses 首轮发送系统提示词（`instructions`）与全部历史，之后用 `previous_response_id` 接续、只发工具结果（`function_call_output`），推理过程由接口保存；`instructions` 每轮都要重新给出
- 检索范围属于对话：管理员可选全部成员，成员只能检索自己的妙记；可再限定时间段或手动选择至多 20 条妙记
- 模型用 `[#记录编号]`、`[#记录编号@mm:ss]` 标注出处，程序按首次出现的顺序改写成 `{{cite:序号}}`、`{{cite:序号@mm:ss}}` 并生成引用来源（纪要链接、妙记链接、`妙记链接?t=毫秒` 的时间点链接）；看不到或不存在的编号直接删掉。历史消息回传给模型前再改回编号写法
- 网页提问走 SSE：依次推送 `chat`、`question`、`step`、`delta`、`reset`、`answer`；`reset` 表示模型转去调用工具，此前的增量作废；没有输出时每 15 秒发一行 `: ping` 注释，避免反向代理按读超时断开。同一对话同时只回答一个问题；请求断开或进程退出时回答以「回答已中断」保存，退出时等回答存好再关数据库
- 对话可以结束，结束后不能再提问。机器人私聊发送「新对话」或「结束」结束当前对话，下一条提问开始新的对话
- 机器人收到提问先回一张进度卡片，回答过程中每 1.2 秒更新一次，完成后换成带引用角标与引用来源的最终卡片；卡片要声明 `update_multi` 才能更新，更新失败时另发一张

## 机器人长连接

- 长连接接入要先 `POST /callback/ws/endpoint`（请求体 `AppID`、`AppSecret`）取 WebSocket 地址，帧是 protobuf 编码的 `Frame`，编解码手写在 `internal/feishu/frame.go`；每个事件帧要在 3 秒内回 `{"code":200}`，否则飞书重推，所以先应答再异步处理，并按消息 ID 去重
- 开发者后台「事件与回调」选「使用长连接接收事件」时，保存前必须已有客户端连上；同一应用的多个客户端同时在线时，每个事件只随机投递给其中一个，本地联调前先停掉线上实例或改用另一个应用
- 断线后从 5 秒开始逐次翻倍重连，最长 2 分钟；设置页显示连接状态

## 结果文档

- 只保存回答的结果，不保存对话过程：正常完成且引用了妙记的回答才整理，出错、中断与没有引用的回答不整理
- 整理时用这个对话的模型再调用一次，提示词是 `internal/chat/result.md`：判断结果类型（待办清单、会议总结、决策记录、进展追踪、风险问题、专题整理），按该类型的版式写正文；没找到内容、反问用户或只有一两句话的回答输出「不保存」，记为已跳过
- 模型沿用回答里的 `[#序号]`、`[#序号@mm:ss]` 出处标记，程序换成跳到纪要或妙记时间点的「〔序号〕」链接；高亮块、分栏、表格里的链接写成 `<a>` 标签，其余地方写成 Markdown 链接
- 文档标题是「类型：主题」，以提问人身份建在其纪要文件夹里，默认仅本人可见；头部引用块写问题、涉及会议、检索范围与提问人，末尾的「引用来源」表格与「模型消耗」由程序填写
- 检索范围按提问时记在用户消息上，对话后来改了范围也不影响
- 回答保存后立即在后台整理，同时最多整理 4 条；正文生成后立即落库，建文档失败重试时直接复用。一条回答最多自动尝试 3 次，间隔 1 分钟与 5 分钟；授权失效不计次数，每 5 分钟再试一次，重新扫码登录后自动补上；建文档结果未知时不再自动重试；只处理 7 天内的回答
- 机器人先发最终的回答卡片，文档建好后在卡片上补上文档链接；网页在有回答整理中时每 3 秒刷新一次对话

## 处理流程

- 搜索起点是该用户上次检查时间再往前 24 小时，首次检查取最近 24 小时，最多回看 30 天，按 token 去重入库。录音类妙记的创建时间是开始录音的时间，录完才出现在搜索结果里，所以重叠要盖住最长的会议
- 有人完成授权或被重新启用时立即检查一次，不等检查间隔
- 用户首次授权之前创建的妙记只登记为已跳过，管理员可在后台手动生成
- 逐字稿为空时每 2 分钟再看一次，不计入尝试次数，入库 6 小时后仍为空则跳过；少于设定字数时跳过
- 一条记录最多自动尝试 3 次，间隔 1 分钟与 5 分钟；模型已产出正文、只是建文档失败时，重试复用正文不再调用模型
- 建文档的请求已被受理但没能确认结果时（轮询超时、进程退出），记录直接标为失败，不自动重试，避免在用户云空间留下重复文档
- 手动重新生成会清空上一次的正文与文档链接，重新调用模型并新建文档；正在生成中的记录不能重新排队
- 每次模型调用的用量记入 `llm_calls` 表：模型、输入 token 与其中的缓存命中、输出 token 与其中的推理、合计、耗时；接口没有返回用量的出错调用不记。重新生成不清除旧记录，后台列表与统计按全部调用累计
- 成员被停用只影响发现新妙记，已排队的记录照常处理
- 发起扫码的两个接口不需要登录，同时进行的扫码流程上限为 20 个
- 纪要生成后私聊只发一条文本消息，内容是文档链接，飞书客户端把它渲染成带正文预览的文档卡片；最终失败时发红色消息卡片

## 纪要模板

内置提示词在 `internal/summary/prompt.md`，可在后台改写。版式参照飞书自带的智能纪要。模型输出的第一行一级标题是会议主题，「参与人：」一行是参与者，这两行由程序取出：文档标题拼成「会议纪要：主题 2026年10月12日」，头部引用块写会议主题、起止时间与参与人。

正文的节标题都是一级标题，依次为：总结、关键决策、待办、风险与卡点、待确认、会议章节，末尾的「相关链接」与「模型消耗」由程序填写，「模型消耗」只写产出这篇文档的那次调用。

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
- 已上线的表改列时，把变更写进 `internal/store/store.go` 的 `columnChanges`，启动时按需补列或删列；`schema` 里的建表语句同步改成目标结构
- 本地联调沿用 `data/` 里已有的数据库：其中已配置测试用的飞书应用（与线上应用分开）、授权与同步自线上的纪要，测试账号的「发现新妙记」保持停用。不要重新扫码建应用或换新的数据目录，授权过期时只重新扫码登录
- 环境变量 `TIRO_FEISHU_OPEN_BASE` 与 `TIRO_FEISHU_ACCOUNTS_BASE` 可以把飞书请求指向本地的模拟服务，用于不依赖真实账号的联调；模拟服务是 `scripts/fake_feishu.py`，同时扮演飞书与模型接口，用法见文件头部
- 默认分支 `master`，提交信息遵循 `git-commit` skill
- 本地开发并自查完成后直接提交、推送 `master` 上线，不另行确认
- 仓库公开：代码、示例数据、注释、文档与提交信息里不写真实的人名、会议内容、公司业务、服务器信息与任何凭据

## 部署

- 推送 `master` 即上线：`.github/workflows/deploy.yml` 在 GitHub 托管的 Runner 上编译前端与二进制，经 SSH 上传到服务器并重启 systemd 服务 `tiro`，等到 `/api/state` 报告本次构建的版本才算成功
- Go 模块与编译缓存由 `setup-go` 按 `go.sum` 缓存，pnpm 依赖仓库按 `web/pnpm-lock.yaml` 缓存
- 服务器地址、登录用户、部署目录与 SSH 密钥存在仓库 Secrets：`SSH_HOST`、`SSH_USER`、`SSH_KEY`、`SSH_KNOWN_HOSTS`、`DEPLOY_PATH`；工作流不输出服务日志
- 部署目录里有当前二进制 `tiro`、上一版 `tiro.prev`、环境文件 `tiro.env`（`TIRO_ADDR`、`TIRO_DATA_DIR`）与数据目录 `data/`；回滚时把 `tiro.prev` 换回 `tiro` 再重启服务
