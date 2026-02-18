// pkg/base/api_httpflv_pull.go
package base

type ApiCtrlStartHttpflvPullReq struct {
    Url           string `json:"url"`          // e.g. https://origin.example.com/live/foo.flv
    AppName       string `json:"app_name"`     // e.g. "live"
    StreamName    string `json:"stream_name"`  // e.g. "foo"
    PullTimeoutMs int    `json:"pull_timeout_ms,omitempty"`
    ReadTimeoutMs int    `json:"read_timeout_ms,omitempty"`
    GopCache      bool   `json:"gop_cache,omitempty"` // reserved for future
}

type ApiCtrlStartHttpflvPullResp struct {
    ErrorCode int    `json:"error_code"`
    Desp      string `json:"desp"`
    Data      struct {
        SessionId  string `json:"session_id"`
        AppName    string `json:"app_name"`
        StreamName string `json:"stream_name"`
    } `json:"data"`
}

type ApiCtrlStopHttpflvPullReq struct {
    AppName    string `json:"app_name,omitempty"`
    StreamName string `json:"stream_name,omitempty"`
    SessionId  string `json:"session_id,omitempty"`
}

type ApiCtrlStopHttpflvPullResp struct {
    ErrorCode int    `json:"error_code"`
    Desp      string `json:"desp"`
}