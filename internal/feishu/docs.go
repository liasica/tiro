package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	// 创建文档转为异步任务后的最长等待时间与轮询间隔
	documentTaskMaxWait      = 5 * time.Minute
	documentTaskPollInterval = 3 * time.Second
)

// ErrDocumentUncertain 表示建文档的请求已被受理，但没能确认最终结果
//
// 此时文档可能已经建好，调用方不应自动重试，否则会在用户云空间里留下重复文档
var ErrDocumentUncertain = errors.New("文档创建结果未知")

// Document 是新建的飞书文档
type Document struct {
	ID  string `json:"document_id"`
	URL string `json:"url"`
}

// CreateFolder 以用户身份在其云空间根目录下新建文件夹，返回文件夹 token
func (c *Client) CreateFolder(ctx context.Context, accessToken, name string) (string, error) {
	var result struct {
		Token string `json:"token"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/create_folder",
		accessToken: accessToken,
		// folder_token 留空表示根目录
		payload: map[string]string{"name": name, "folder_token": ""},
	}, &result)
	if err != nil {
		return "", err
	}

	if result.Token == "" {
		return "", errors.New("飞书没有返回新建文件夹的 token")
	}

	return result.Token, nil
}

// documentTask 是创建文档的异步任务
type documentTask struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	PollAfterMS int    `json:"poll_after_ms"`
	Result      *struct {
		CreateDocument string `json:"create_document"`
	} `json:"result"`
	Failure *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"failure"`
}

// documentResponse 是创建文档接口的两种返回形态：直接给出文档，或给出异步任务
type documentResponse struct {
	Document *Document     `json:"document"`
	Task     *documentTask `json:"task"`
}

// CreateDocument 以用户身份用 Markdown 内容新建文档，folderToken 为空时建在云空间根目录
func (c *Client) CreateDocument(ctx context.Context, accessToken string, draft DocumentDraft) (*Document, error) {
	var title bytes.Buffer
	if err := xml.EscapeText(&title, []byte(draft.Title)); err != nil {
		return nil, fmt.Errorf("编码文档标题失败: %w", err)
	}

	payload := map[string]string{
		"format":  "markdown",
		"content": "<title>" + title.String() + "</title>\n" + draft.Markdown,
	}

	if draft.FolderToken != "" {
		payload["parent_token"] = draft.FolderToken
	}

	var response documentResponse

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/docs_ai/v1/documents",
		accessToken: accessToken,
		payload:     payload,
	}, &response)
	if err != nil {
		return nil, err
	}

	if response.Task != nil && response.Task.TaskID != "" {
		return c.waitDocumentTask(ctx, accessToken, c.originOr(draft.Origin), response.Task)
	}

	return finishDocument(c.originOr(draft.Origin), response.Document)
}

// originOr 返回拼文档链接用的域名，没有租户域名时退回开放平台的域名
func (c *Client) originOr(origin string) string {
	if origin == "" {
		return c.openBase
	}

	return origin
}

// DocumentDraft 是待创建文档的标题、正文与存放位置
type DocumentDraft struct {
	Title       string
	Markdown    string
	FolderToken string
	// Origin 是租户的访问域名，形如 https://example.feishu.cn，接口没有返回文档链接时用它拼
	Origin string
}

// waitDocumentTask 轮询异步任务直到文档建好
//
// 任务已经受理之后的超时、取消与查询失败都归为结果未知
func (c *Client) waitDocumentTask(ctx context.Context, accessToken, origin string, task *documentTask) (*Document, error) {
	deadline := time.Now().Add(documentTaskMaxWait)

	for {
		switch task.Status {
		case "success", "succeeded", "done":
			if task.Result == nil || task.Result.CreateDocument == "" {
				return nil, errors.New("创建文档的任务成功了，但没有返回文档信息")
			}

			var response documentResponse
			if err := json.Unmarshal([]byte(task.Result.CreateDocument), &response); err != nil {
				return nil, fmt.Errorf("解析创建文档任务的结果失败: %w", err)
			}

			return finishDocument(origin, response.Document)
		case "failed", "expired":
			if task.Failure != nil && task.Failure.Message != "" {
				return nil, fmt.Errorf("创建文档失败: %s", task.Failure.Message)
			}

			return nil, errors.New("创建文档失败")
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: 等待超过 %s", ErrDocumentUncertain, documentTaskMaxWait)
		}

		interval := documentTaskPollInterval

		if task.PollAfterMS > 0 {
			interval = time.Duration(task.PollAfterMS) * time.Millisecond
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrDocumentUncertain, ctx.Err())
		case <-time.After(interval):
		}

		var response documentResponse

		err := c.openAPI(ctx, apiRequest{
			method:      http.MethodGet,
			path:        "/open-apis/docs_ai/v1/async_tasks/" + url.PathEscape(task.TaskID),
			accessToken: accessToken,
		}, &response)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDocumentUncertain, err)
		}

		if response.Task == nil {
			return nil, fmt.Errorf("%w: 查询任务时没有返回任务信息", ErrDocumentUncertain)
		}

		task = response.Task
	}
}

// finishDocument 校验文档信息，接口没给链接时按文档 ID 拼出链接
func finishDocument(origin string, document *Document) (*Document, error) {
	if document == nil || document.ID == "" {
		return nil, errors.New("飞书没有返回新建文档的信息")
	}

	if document.URL == "" {
		document.URL = origin + "/docx/" + url.PathEscape(document.ID)
	}

	return document, nil
}
