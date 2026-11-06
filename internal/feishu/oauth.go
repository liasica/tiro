package feishu

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	pathAppRegistration     = "/oauth/v1/app/registration"
	pathDeviceAuthorization = "/oauth/v1/device_authorization"
	pathOAuthToken          = "/oauth/v3/token"

	// 扫码建应用的页面按接入方版本号放行，取值与飞书官方命令行工具的参数一致
	registrationClientVersion = "1.0.99"
)

// UserScopes 是授权时申请的用户权限
var UserScopes = []string{
	"minutes:minutes.search:read",
	"minutes:minutes.basic:read",
	"minutes:minutes.artifacts:read",
	"docx:document:create",
	"space:folder:create",
	"offline_access",
}

// 扫码流程轮询时的中间状态
var (
	// ErrAuthorizationPending 表示用户还没有在飞书里确认
	ErrAuthorizationPending = errors.New("等待用户确认")
	// ErrSlowDown 表示轮询过快，需要拉长间隔
	ErrSlowDown = errors.New("轮询过快")
	// ErrAccessDenied 表示用户拒绝了请求
	ErrAccessDenied = errors.New("用户拒绝了请求")
	// ErrFlowExpired 表示二维码已过期
	ErrFlowExpired = errors.New("二维码已过期")
)

// DeviceFlow 是一次扫码流程的起始信息
type DeviceFlow struct {
	DeviceCode      string
	VerificationURL string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

// AppCredential 是扫码建应用后拿到的应用凭据
type AppCredential struct {
	AppID     string
	AppSecret string
}

// Token 是用户访问令牌与刷新令牌
type Token struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// UserInfo 是授权用户的基本资料
type UserInfo struct {
	OpenID    string `json:"open_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// BeginAppRegistration 发起扫码建应用，返回给用户扫的链接
func (c *Client) BeginAppRegistration(ctx context.Context) (*DeviceFlow, error) {
	form := url.Values{}
	form.Set("action", "begin")
	form.Set("archetype", "PersonalAgent")
	form.Set("auth_method", "client_secret")
	form.Set("request_user_info", "open_id tenant_brand")

	data, err := c.oauthForm(ctx, pathAppRegistration, form, "")
	if err != nil {
		return nil, err
	}

	deviceCode := stringField(data, "device_code")
	userCode := stringField(data, "user_code")

	if deviceCode == "" || userCode == "" {
		return nil, errors.New("飞书没有返回建应用所需的设备码")
	}

	query := url.Values{}
	query.Set("user_code", userCode)
	query.Set("lpv", registrationClientVersion)
	query.Set("ocv", registrationClientVersion)
	query.Set("from", "cli")

	expiresIn := intField(data, "expire_in", 0)
	if expiresIn == 0 {
		expiresIn = intField(data, "expires_in", 600)
	}

	return &DeviceFlow{
		DeviceCode:      deviceCode,
		VerificationURL: c.openBase + "/page/cli?" + query.Encode(),
		ExpiresIn:       time.Duration(expiresIn) * time.Second,
		Interval:        time.Duration(intField(data, "interval", 5)) * time.Second,
	}, nil
}

// PollAppRegistration 查询扫码建应用的结果
//
// 用户尚未确认时返回 ErrAuthorizationPending，其余中间状态见本包的错误变量
func (c *Client) PollAppRegistration(ctx context.Context, deviceCode string) (*AppCredential, error) {
	form := url.Values{}
	form.Set("action", "poll")
	form.Set("device_code", deviceCode)

	data, err := c.oauthForm(ctx, pathAppRegistration, form, "")
	if err != nil {
		return nil, translateFlowError(err)
	}

	if info, ok := data["user_info"].(map[string]any); ok && stringField(info, "tenant_brand") == "lark" {
		return nil, errors.New("暂不支持 Lark 国际版租户")
	}

	credential := &AppCredential{
		AppID:     stringField(data, "client_id"),
		AppSecret: stringField(data, "client_secret"),
	}

	if credential.AppID == "" || credential.AppSecret == "" {
		return nil, ErrAuthorizationPending
	}

	return credential, nil
}

// BeginDeviceAuthorization 发起用户扫码授权
func (c *Client) BeginDeviceAuthorization(ctx context.Context, credential AppCredential) (*DeviceFlow, error) {
	form := url.Values{}
	form.Set("client_id", credential.AppID)
	form.Set("scope", strings.Join(UserScopes, " "))

	basicAuth := base64.StdEncoding.EncodeToString([]byte(credential.AppID + ":" + credential.AppSecret))

	data, err := c.oauthForm(ctx, pathDeviceAuthorization, form, basicAuth)
	if err != nil {
		return nil, err
	}

	verificationURL := stringField(data, "verification_uri_complete")
	if verificationURL == "" {
		verificationURL = stringField(data, "verification_uri")
	}

	deviceCode := stringField(data, "device_code")
	if deviceCode == "" || verificationURL == "" {
		return nil, errors.New("飞书没有返回授权所需的设备码")
	}

	return &DeviceFlow{
		DeviceCode:      deviceCode,
		VerificationURL: verificationURL,
		ExpiresIn:       time.Duration(intField(data, "expires_in", 240)) * time.Second,
		Interval:        time.Duration(intField(data, "interval", 5)) * time.Second,
	}, nil
}

// PollDeviceToken 查询用户扫码授权的结果，成功时返回令牌
func (c *Client) PollDeviceToken(ctx context.Context, credential AppCredential, deviceCode string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	form.Set("client_id", credential.AppID)
	form.Set("client_secret", credential.AppSecret)

	data, err := c.oauthForm(ctx, pathOAuthToken, form, "")
	if err != nil {
		return nil, translateFlowError(err)
	}

	return tokenFromResponse(data)
}

// RefreshToken 用刷新令牌换取新的令牌
//
// 刷新令牌只能使用一次，调用方必须把返回的新令牌落库
func (c *Client) RefreshToken(ctx context.Context, credential AppCredential, refreshToken string) (*Token, error) {
	data, err := c.oauthJSON(ctx, pathOAuthToken, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     credential.AppID,
		"client_secret": credential.AppSecret,
	})
	if err != nil {
		return nil, err
	}

	return tokenFromResponse(data)
}

// IsAuthorizationInvalid 判断错误是否意味着用户必须重新授权
func IsAuthorizationInvalid(err error) bool {
	var oauthErr *OAuthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.Code {
		case "invalid_grant", "invalid_token", "access_denied", "unauthorized_client":
			return true
		}
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// 99991668、99991677 等为用户令牌无效或过期，20064 之后的一段为刷新令牌失效
		switch apiErr.Code {
		case 99991668, 99991677, 99991663, 20026, 20037, 20064, 20073, 20074:
			return true
		}

		return apiErr.HTTPStatus == http.StatusUnauthorized
	}

	return false
}

func tokenFromResponse(data map[string]any) (*Token, error) {
	accessToken := stringField(data, "access_token")
	if accessToken == "" {
		return nil, errors.New("飞书没有返回访问令牌")
	}

	now := time.Now()

	return &Token{
		AccessToken:      accessToken,
		RefreshToken:     stringField(data, "refresh_token"),
		AccessExpiresAt:  now.Add(time.Duration(intField(data, "expires_in", 7200)) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(intField(data, "refresh_token_expires_in", 604800)) * time.Second),
	}, nil
}

// translateFlowError 把扫码流程里的 OAuth 错误码转成本包的状态错误
func translateFlowError(err error) error {
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) {
		return err
	}

	switch oauthErr.Code {
	case "authorization_pending":
		return ErrAuthorizationPending
	case "slow_down":
		return ErrSlowDown
	case "access_denied":
		return ErrAccessDenied
	case "expired_token", "invalid_grant":
		return ErrFlowExpired
	}

	return err
}

// UserInfo 读取访问令牌所属用户的资料
func (c *Client) UserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	var info UserInfo

	err := c.openAPI(ctx, apiRequest{
		method:      http.MethodGet,
		path:        "/open-apis/authen/v1/user_info",
		accessToken: accessToken,
	}, &info)
	if err != nil {
		return nil, err
	}

	if info.OpenID == "" {
		return nil, errors.New("飞书没有返回用户的 open_id")
	}

	return &info, nil
}

// TenantToken 返回应用身份的访问令牌，带内存缓存
func (c *Client) TenantToken(ctx context.Context, credential AppCredential) (string, error) {
	c.tenantMutex.Lock()
	defer c.tenantMutex.Unlock()

	if c.tenantAppID == credential.AppID && time.Until(c.tenantExpiresAt) > 5*time.Minute {
		return c.tenantToken, nil
	}

	// 这个接口的令牌字段在响应顶层，不在 data 里，单独解析
	var result struct {
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}

	err := c.tenantTokenRequest(ctx, credential, &result)
	if err != nil {
		return "", err
	}

	c.tenantAppID = credential.AppID
	c.tenantToken = result.TenantAccessToken
	c.tenantExpiresAt = time.Now().Add(time.Duration(result.Expire) * time.Second)

	return c.tenantToken, nil
}
