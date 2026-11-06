package feishu

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// 搜索接口单页条数上限与一次检查最多翻的页数
const (
	searchPageSize = 30
	searchMaxPages = 20
)

// MinuteInfo 是妙记的基本信息
type MinuteInfo struct {
	Token     string
	Title     string
	URL       string
	OwnerID   string
	CreatedAt time.Time
	Duration  time.Duration
}

// MinuteArtifacts 是妙记的 AI 产物与逐字稿
type MinuteArtifacts struct {
	Summary    string `json:"summary"`
	Transcript string `json:"transcript"`
}

// SearchMinuteTokens 搜索某个用户名下在 since 之后创建的妙记，返回 token 列表
//
// 新妙记生成后约 6 分钟才会出现在搜索结果里
func (c *Client) SearchMinuteTokens(ctx context.Context, accessToken, ownerOpenID string, since time.Time) ([]string, error) {
	payload := map[string]any{
		"filter": map[string]any{
			"create_time": map[string]string{"start_time": since.Format(time.RFC3339)},
			"owner_ids":   []string{ownerOpenID},
		},
		"sorter": "create_time_desc",
	}

	var (
		minuteTokens []string
		pageToken    string
	)

	for range searchMaxPages {
		query := url.Values{}
		query.Set("page_size", strconv.Itoa(searchPageSize))

		if pageToken != "" {
			query.Set("page_token", pageToken)
		}

		var page struct {
			Items []struct {
				Token string `json:"token"`
			} `json:"items"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		}

		err := c.openAPI(ctx, apiRequest{
			method:      http.MethodPost,
			path:        "/open-apis/minutes/v1/minutes/search",
			accessToken: accessToken,
			query:       query,
			payload:     payload,
		}, &page)
		if err != nil {
			return nil, err
		}

		for _, item := range page.Items {
			if item.Token != "" {
				minuteTokens = append(minuteTokens, item.Token)
			}
		}

		if !page.HasMore || page.PageToken == "" {
			break
		}

		pageToken = page.PageToken
	}

	return minuteTokens, nil
}

// MinuteInfo 读取妙记的标题、链接、创建时间与时长
func (c *Client) MinuteInfo(ctx context.Context, accessToken, minuteToken string) (*MinuteInfo, error) {
	var result struct {
		Minute struct {
			Token      string `json:"token"`
			Title      string `json:"title"`
			URL        string `json:"url"`
			OwnerID    string `json:"owner_id"`
			CreateTime string `json:"create_time"`
			Duration   string `json:"duration"`
		} `json:"minute"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodGet,
		path:        "/open-apis/minutes/v1/minutes/" + url.PathEscape(minuteToken),
		accessToken: accessToken,
	}, &result)
	if err != nil {
		return nil, err
	}

	info := &MinuteInfo{
		Token:   minuteToken,
		Title:   result.Minute.Title,
		URL:     result.Minute.URL,
		OwnerID: result.Minute.OwnerID,
	}

	// 创建时间与时长都是毫秒数的字符串
	if milliseconds := parseMilliseconds(result.Minute.CreateTime); milliseconds > 0 {
		info.CreatedAt = time.UnixMilli(milliseconds)
	}

	info.Duration = time.Duration(parseMilliseconds(result.Minute.Duration)) * time.Millisecond

	return info, nil
}

// parseMilliseconds 解析毫秒数的字符串，不是数字时返回 0
func parseMilliseconds(text string) int64 {
	milliseconds, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0
	}

	return milliseconds
}

// MinuteArtifacts 读取妙记的 AI 总结与逐字稿
func (c *Client) MinuteArtifacts(ctx context.Context, accessToken, minuteToken string) (*MinuteArtifacts, error) {
	var artifacts MinuteArtifacts

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodGet,
		path:        "/open-apis/minutes/v1/minutes/" + url.PathEscape(minuteToken) + "/artifacts",
		accessToken: accessToken,
	}, &artifacts)
	if err != nil {
		return nil, err
	}

	return &artifacts, nil
}
