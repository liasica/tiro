package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// ModelInfo 是接口提供的一个模型
type ModelInfo struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
}

// ListModels 读取接口提供的模型列表，按发布时间从新到旧排序，时间相同时按名称排序
func ListModels(ctx context.Context, config Config) ([]ModelInfo, error) {
	if config.BaseURL == "" {
		return nil, errors.New("接口地址不能为空")
	}

	endpoint := strings.TrimRight(config.BaseURL, "/") + "/models"

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("模型接口地址无效: %w", err)
	}

	if config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	var response *http.Response

	response, err = httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("请求模型接口失败: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	var raw []byte

	raw, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	if response.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("模型接口返回 %d: %s", response.StatusCode, snippet(raw))
	}

	var list struct {
		Data []ModelInfo `json:"data"`
	}

	if err = json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("模型接口返回了无法识别的模型列表: %s", snippet(raw))
	}

	models := slices.DeleteFunc(list.Data, func(model ModelInfo) bool { return model.ID == "" })

	slices.SortFunc(models, func(a, b ModelInfo) int {
		return cmp.Or(cmp.Compare(b.Created, a.Created), strings.Compare(a.ID, b.ID))
	})

	return models, nil
}
