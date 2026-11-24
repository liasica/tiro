package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// 多维表格的字段类型
const (
	FieldText     = 1
	FieldNumber   = 2
	FieldSelect   = 3
	FieldDate     = 5
	FieldUser     = 11
	FieldURL      = 15
	FieldLink     = 18
	dateFormatter = "yyyy/MM/dd HH:mm"
)

// Base 是新建的多维表格
type Base struct {
	Token          string
	URL            string
	DefaultTableID string
}

// BaseField 是建数据表时的一个字段，第一个字段是主字段
type BaseField struct {
	Name     string `json:"field_name"`
	Type     int    `json:"type"`
	Property any    `json:"property,omitempty"`
}

// TextField 返回多行文本字段
func TextField(name string) BaseField {
	return BaseField{Name: name, Type: FieldText}
}

// NumberField 返回整数格式的数字字段
func NumberField(name string) BaseField {
	return BaseField{Name: name, Type: FieldNumber, Property: map[string]string{"formatter": "0"}}
}

// DateField 返回精确到分钟的日期字段
func DateField(name string) BaseField {
	return BaseField{Name: name, Type: FieldDate, Property: map[string]any{"date_formatter": dateFormatter, "auto_fill": false}}
}

// SelectField 返回单选字段
func SelectField(name string, options ...string) BaseField {
	choices := make([]map[string]string, 0, len(options))
	for _, option := range options {
		choices = append(choices, map[string]string{"name": option})
	}

	return BaseField{Name: name, Type: FieldSelect, Property: map[string]any{"options": choices}}
}

// UserField 返回单个人员字段
func UserField(name string) BaseField {
	return BaseField{Name: name, Type: FieldUser, Property: map[string]bool{"multiple": false}}
}

// URLField 返回超链接字段
func URLField(name string) BaseField {
	return BaseField{Name: name, Type: FieldURL}
}

// LinkField 返回关联到同一多维表格里另一张数据表的单向关联字段，可以关联多条记录
func LinkField(name, tableID string) BaseField {
	return BaseField{Name: name, Type: FieldLink, Property: map[string]any{"table_id": tableID, "multiple": true}}
}

// URLValue 返回超链接字段的取值
func URLValue(text, link string) map[string]string {
	return map[string]string{"text": text, "link": link}
}

// UserValue 返回人员字段的取值
func UserValue(openID string) []map[string]string {
	return []map[string]string{{"id": openID}}
}

// CreateBase 以用户身份在文件夹里新建多维表格，folderToken 为空时建在云空间根目录
func (c *Client) CreateBase(ctx context.Context, accessToken, name, folderToken string) (*Base, error) {
	var result struct {
		App struct {
			AppToken       string `json:"app_token"`
			URL            string `json:"url"`
			DefaultTableID string `json:"default_table_id"`
		} `json:"app"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/bitable/v1/apps",
		accessToken: accessToken,
		payload:     map[string]string{"name": name, "folder_token": folderToken},
	}, &result)
	if err != nil {
		return nil, err
	}

	if result.App.AppToken == "" {
		return nil, errors.New("飞书没有返回多维表格的 token")
	}

	return &Base{Token: result.App.AppToken, URL: result.App.URL, DefaultTableID: result.App.DefaultTableID}, nil
}

// BaseTable 是要新建的数据表
type BaseTable struct {
	Name     string
	ViewName string
	Fields   []BaseField
}

// CreateBaseTable 在多维表格里新建数据表，返回数据表 ID
func (c *Client) CreateBaseTable(ctx context.Context, accessToken, baseToken string, table BaseTable) (string, error) {
	var result struct {
		TableID string `json:"table_id"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/bitable/v1/apps/" + url.PathEscape(baseToken) + "/tables",
		accessToken: accessToken,
		payload: map[string]any{
			"table": map[string]any{"name": table.Name, "default_view_name": table.ViewName, "fields": table.Fields},
		},
	}, &result)
	if err != nil {
		return "", err
	}

	if result.TableID == "" {
		return "", errors.New("飞书没有返回数据表 ID")
	}

	return result.TableID, nil
}

// DeleteBaseTable 删除多维表格里的数据表
func (c *Client) DeleteBaseTable(ctx context.Context, accessToken, baseToken, tableID string) error {
	return c.openAPI(ctx, apiRequest{
		method:      http.MethodDelete,
		path:        "/open-apis/bitable/v1/apps/" + url.PathEscape(baseToken) + "/tables/" + url.PathEscape(tableID),
		accessToken: accessToken,
	}, nil)
}

// BaseRecord 是要新增的一条记录
type BaseRecord struct {
	TableID string
	// ClientToken 是 UUID 格式的幂等标识，同一个值重复提交只会新增一条
	ClientToken string
	Fields      map[string]any
}

// CreateBaseRecord 在数据表里新增一条记录，返回记录 ID；人员字段按 open_id 取值
func (c *Client) CreateBaseRecord(ctx context.Context, accessToken, baseToken string, record BaseRecord) (string, error) {
	query := url.Values{}
	query.Set("user_id_type", "open_id")

	if record.ClientToken != "" {
		query.Set("client_token", record.ClientToken)
	}

	var result struct {
		Record struct {
			RecordID string `json:"record_id"`
		} `json:"record"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/bitable/v1/apps/" + url.PathEscape(baseToken) + "/tables/" + url.PathEscape(record.TableID) + "/records",
		accessToken: accessToken,
		query:       query,
		payload:     map[string]any{"fields": record.Fields},
	}, &result)
	if err != nil {
		return "", err
	}

	if result.Record.RecordID == "" {
		return "", errors.New("飞书没有返回记录 ID")
	}

	return result.Record.RecordID, nil
}

// FindBaseTable 按名称找数据表，找不到时返回空串
func (c *Client) FindBaseTable(ctx context.Context, accessToken, baseToken, name string) (string, error) {
	query := url.Values{}
	query.Set("page_size", "100")

	var result struct {
		Items []struct {
			TableID string `json:"table_id"`
			Name    string `json:"name"`
		} `json:"items"`
	}

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodGet,
		path:        "/open-apis/bitable/v1/apps/" + url.PathEscape(baseToken) + "/tables",
		accessToken: accessToken,
		query:       query,
	}, &result)
	if err != nil {
		return "", err
	}

	for _, item := range result.Items {
		if item.Name == name {
			return item.TableID, nil
		}
	}

	return "", nil
}

// ClientToken 由几段文字生成固定的 UUID 格式幂等标识，同样的输入总是得到同样的值
func ClientToken(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	// 按 UUID 第 4 版的格式设置版本与变体位
	sum[6] = sum[6]&0x0f | 0x40
	sum[8] = sum[8]&0x3f | 0x80

	encoded := hex.EncodeToString(sum[:16])

	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// IsBaseMissing 判断错误是否因为多维表格或数据表已经不存在
func IsBaseMissing(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	// 1254040 为多维表格不存在，1254041 为数据表不存在
	if apiErr.Code == 1254040 || apiErr.Code == 1254041 || apiErr.HTTPStatus == http.StatusNotFound {
		return true
	}

	message := strings.ToLower(apiErr.Message)

	return strings.Contains(message, "basetokennotfound") || strings.Contains(message, "tableidnotfound")
}

// IsDuplicateName 判断错误是否因为数据表或字段重名
func IsDuplicateName(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	return strings.Contains(strings.ToLower(apiErr.Message), "duplicat")
}

// IsScopeMissing 判断错误是否因为应用没有开通或用户没有授予所需的权限
func IsScopeMissing(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	// 99991672 为应用未开通权限，99991679 为用户未授予权限
	return apiErr.Code == 99991672 || apiErr.Code == 99991679
}

// CreateBaseField 在数据表末尾新增一个字段
func (c *Client) CreateBaseField(ctx context.Context, accessToken, baseToken, tableID string, field BaseField) error {
	return c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/bitable/v1/apps/" + url.PathEscape(baseToken) + "/tables/" + url.PathEscape(tableID) + "/fields",
		accessToken: accessToken,
		payload:     field,
	}, nil)
}
