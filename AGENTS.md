# Tiro 项目记忆

## 定位

Tiro 把飞书妙记自动整理成会议纪要：发现授权用户名下新生成的妙记，取回逐字稿与 AI 产物，交给大模型按模板写成纪要，在该用户的飞书云空间创建文档，再由机器人私聊把链接发给本人。

## 约束

- 触发方式是定时搜索新妙记，不依赖事件
- 纪要生成走 OpenAI 格式的接口，URL、token、模型名都是配置项
- 纪要文档以授权人身份创建在其本人云空间，默认仅本人可见
- 妙记相关接口一律以用户身份调用，每个使用者授权一次
- 飞书应用凭据、用户令牌、模型接口的 token 只出现在被 `.gitignore` 忽略的本地配置与数据文件里

## 飞书妙记接口

| 用途 | 接口 | 权限 |
| --- | --- | --- |
| 找新妙记 | `POST /open-apis/minutes/v1/minutes/search` | `minutes:minutes.search:read` |
| 基本信息 | `GET /open-apis/minutes/v1/minutes/{minute_token}` | `minutes:minutes.basic:read` |
| 内容 | `GET /open-apis/minutes/v1/minutes/{minute_token}/artifacts` | `minutes:minutes.artifacts:read` |

- 搜索可按 `owner_ids` 与 `create_time.start_time` 过滤，`sorter` 用 `create_time_desc`；结果项只有 `token`、`display_info`、`meta_data`，标题、创建时间、时长、状态要从基本信息接口取
- 新妙记生成约 6 分钟后才能被搜到；内容接口在妙记生成后立即可取
- `artifacts` 返回 `summary`、`keywords`、`transcript`，`todos` 与 `chapters` 不一定有，纪要以逐字稿为准
- 逐字稿每段是 `Speaker N 时间戳` 一行加正文一行，没有真人姓名
- 录音转写与外部设备上传的妙记都能搜到；上传的妙记 `create_time` 是上传时间
- `minutes.minute.generated_v1` 事件在开发者后台无法添加，订阅接口 `POST /open-apis/minutes/v1/minutes/subscription` 返回成功但事件不送达

## 纪要模板

文档结构依次为：头部（主题、时间与时长、参与人、妙记链接）、结论、决策、待办、进展、风险与卡点、待确认。

- 专有名词按术语表纠正转写错误
- 待办写明负责人与具体截止日期，负责人对不上时标「待确认」
- 同一件事只写一次，出处时间戳标在条目后
- 逻辑存疑或转写含糊的内容归入「待确认」
- 不写金句、反馈收集这类低信息量内容

## 开发约定

- 默认分支 `master`，提交信息遵循 `git-commit` skill
