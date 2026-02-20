package base

type ApiCtrlStartWsflvPullReq struct {
	Url           string            `json:"url"`
	AppName       string            `json:"app_name"`
	StreamName    string            `json:"stream_name"`
	PullTimeoutMs int               `json:"pull_timeout_ms,omitempty"`
	ReadTimeoutMs int               `json:"read_timeout_ms,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
}

type ApiCtrlStopWsflvPullReq struct {
	SessionId  string `json:"session_id,omitempty"`
	AppName    string `json:"app_name,omitempty"`
	StreamName string `json:"stream_name,omitempty"`
}

type ApiCtrlStartWsflvPullResp struct {
	ErrorCode int    `json:"error_code"`
	Desp      string `json:"desp"`
	Data      struct {
		SessionId  string `json:"session_id"`
		AppName    string `json:"app_name"`
		StreamName string `json:"stream_name"`
	} `json:"data"`
}

type ApiCtrlStopWsflvPullResp struct {
	ErrorCode int    `json:"error_code"`
	Desp      string `json:"desp"`
	Data      struct {
		SessionId string `json:"session_id"`
	} `json:"data"`
}
