// Package hostbridge 是插件调用宿主回调的唯一通道。
//
// 所有出站 HTTP 都必须经宿主（host.http.do / do_stream），否则会绕过全局代理、
// 账号级代理、出站请求日志与用量统计。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Caller 是宿主回调的底层实现：package main 在 init 里注入 CGO 版本。
type Caller func(callbackID, method string, payload any) (json.RawMessage, error)

var (
	callerMu sync.RWMutex
	caller   Caller

	gateMu       sync.Mutex
	shuttingDown bool
	inflightWG   sync.WaitGroup
	gate         = make(chan struct{}, 64)
)

// ErrNotConfigured 表示宿主回调尚未注入。
var ErrNotConfigured = errors.New("hostbridge: caller is not configured")

// Configure 注入宿主回调实现。
func Configure(impl Caller) {
	callerMu.Lock()
	caller = impl
	callerMu.Unlock()
}

func current() (Caller, error) {
	callerMu.RLock()
	impl := caller
	callerMu.RUnlock()
	if impl == nil {
		return nil, ErrNotConfigured
	}
	return impl, nil
}

func acquire() error {
	gateMu.Lock()
	if shuttingDown {
		gateMu.Unlock()
		return errors.New("host callbacks unavailable: plugin is shutting down")
	}
	inflightWG.Add(1)
	gateMu.Unlock()
	gate <- struct{}{}
	return nil
}

func release() {
	<-gate
	inflightWG.Done()
}

// Call 发起一次宿主回调。callbackID 为空表示后台任务。
func Call(callbackID, method string, payload any) (json.RawMessage, error) {
	if errAcquire := acquire(); errAcquire != nil {
		return nil, errAcquire
	}
	defer release()
	impl, errCaller := current()
	if errCaller != nil {
		return nil, errCaller
	}
	return impl(callbackID, method, payload)
}

// WaitForShutdown 关闭准入并等待在飞回调返回。
// 宿主回调不可取消，提前返回会在活跃 CGO 栈上 dlclose 导致崩溃。
func WaitForShutdown() {
	gateMu.Lock()
	shuttingDown = true
	gateMu.Unlock()
	inflightWG.Wait()
}

// ResetForInit 重新打开准入。
//
// 宿主重载插件时会先 cliproxyPluginShutdown 再 cliproxy_plugin_init，
// 但同一份 .dll 可能留在进程内复用（Windows 不保证 dlclose 真的卸载）。
// 不复位的话，第二次 init 后所有宿主回调都会报 "plugin is shutting down"，
// 表现为插件注册正常、status 可读，但任何需要访问宿主的操作（登录、执行）全挂。
func ResetForInit() {
	gateMu.Lock()
	shuttingDown = false
	gateMu.Unlock()
}

type callbackIDKey struct{}

// WithCallbackID 把宿主回调 ID 绑定到 context。
func WithCallbackID(ctx context.Context, callbackID string) context.Context {
	if callbackID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callbackIDKey{}, callbackID)
}

// CallbackID 从 context 取回调 ID。
func CallbackID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callbackIDKey{}).(string)
	return id
}

// CallCtx 从 context 取回调 ID 并发起调用。
func CallCtx(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	return Call(CallbackID(ctx), method, payload)
}

// MarshalPayload 序列化载荷并按需注入 host_callback_id。
func MarshalPayload(callbackID string, payload any) ([]byte, error) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload: %w", errMarshal)
	}
	if callbackID == "" {
		return raw, nil
	}
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host callback payload: %w", errUnmarshal)
		}
	}
	encodedID, errID := json.Marshal(callbackID)
	if errID != nil {
		return nil, errID
	}
	fields["host_callback_id"] = encodedID
	return json.Marshal(fields)
}

// DecodeEnvelope 解开宿主回调返回的信封，取出 result。
func DecodeEnvelope(raw []byte) (json.RawMessage, error) {
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode envelope: %w", errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, errors.New("host callback failed")
	}
	return append(json.RawMessage(nil), env.Result...), nil
}
