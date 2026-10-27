package core

// 插件与宿主之间的错误与信封契约。
//
// HTTPStatus 是最重要的字段：宿主按它决定凭证处置
// （401 禁用凭证 / 429 短冷却 / 402 长冷却 / 5xx 换号重试）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// PluginError 是插件内部的结构化错误。
type PluginError struct {
	Code       string
	Message    string
	Retryable  bool
	HTTPStatus int
}

func (e *PluginError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func NewPluginError(code, message string, status int) *PluginError {
	return &PluginError{Code: code, Message: message, HTTPStatus: status}
}

func RetryablePluginError(code, message string, status int) *PluginError {
	return &PluginError{Code: code, Message: message, Retryable: true, HTTPStatus: status}
}

// UpstreamError 是上游错误分类后的形态。
type UpstreamError struct {
	Kind      string // auth / rate_limit / quota / server / network / bad_request
	Msg       string
	Status    int
	Retryable bool
}

func (e *UpstreamError) Error() string { return e.Msg }

// ToPluginError 把上游错误翻译成宿主认得的错误。
func ToPluginError(err error) error {
	if err == nil {
		return nil
	}
	var up *UpstreamError
	if errors.As(err, &up) {
		status := up.Status
		switch up.Kind {
		case "auth":
			status = http.StatusUnauthorized
			up.Retryable = false
		case "rate_limit":
			status = http.StatusTooManyRequests
			up.Retryable = true
		case "quota":
			status = http.StatusPaymentRequired
			up.Retryable = true
		case "bad_request":
			if status == 0 {
				status = http.StatusBadRequest
			}
			up.Retryable = false
		case "server", "network":
			status = http.StatusBadGateway
			up.Retryable = true
		}
		code := "upstream_" + up.Kind
		if up.Retryable {
			return RetryablePluginError(code, up.Msg, status)
		}
		return NewPluginError(code, up.Msg, status)
	}
	var pe *PluginError
	if errors.As(err, &pe) {
		return pe
	}
	return RetryablePluginError("upstream_request_failed", err.Error(), http.StatusBadGateway)
}

// envelope 与宿主 pluginabi.Envelope 对齐。
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// OKEnvelope 构造成功信封。
func OKEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal result: %w", errMarshal)
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

// ErrorEnvelopeFromError 把错误转成失败信封并保留 HTTP 状态码。
func ErrorEnvelopeFromError(err error) []byte {
	if err == nil {
		return ErrorEnvelope("plugin_error", "unknown error")
	}
	converted := ToPluginError(err)
	if pe, ok := converted.(*PluginError); ok {
		raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
			Code:       pe.Code,
			Message:    pe.Message,
			Retryable:  pe.Retryable,
			HTTPStatus: pe.HTTPStatus,
		}})
		return raw
	}
	return ErrorEnvelope("plugin_error", converted.Error())
}

// ErrorEnvelope 构造无状态码的失败信封（宿主按 500 处理）。
func ErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}
