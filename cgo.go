package main

// CPA 插件宿主的 C ABI 与宿主回调的 CGO 实现。
//
// 宿主 dlopen 后调用 cliproxy_plugin_init 传入两张函数表；之后每次 RPC 走
// cliproxyPluginCall，插件发起的宿主回调走 nativeHostCallScoped。
// 结构体布局必须与宿主 pluginhost 的 windowsHostAPI / windowsPluginAPI 逐字节一致。

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

// 必须是 static：cgo 会把 preamble 复制进多个生成文件，
// 非 static 定义会导致重复符号链接失败。
static int cpa_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (api == NULL || api->call == NULL) {
		return 1;
	}
	return api->call(api->host_ctx, method, request, request_len, response);
}

static void cpa_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	if (api != NULL && api->free_buffer != NULL && ptr != NULL) {
		api->free_buffer(ptr, len);
	}
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"cpa-today-plugin/cpasdk/pluginabi"
	"cpa-today-plugin/internal/app"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
)

var hostAPI *C.cliproxy_host_api

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	// 同一份 DLL 可能被复用：init 必须重新打开回调准入。
	hostbridge.ResetForInit()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, core.ErrorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := pluginApp.HandleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, core.ErrorEnvelopeFromError(errHandle))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// 只取消在途流；不锁死回调准入、不清 hostAPI。
	//
	// 宿主在配置变更、插件重载时会调 shutdown，随后可能对同一份 DLL 再次
	// init（Windows 不保证 dlclose 真的卸载）。上一版在这里永久关闭准入并
	// 置空 hostAPI，导致重载后插件看起来"注册成功、status 可读"，但任何
	// 需要访问宿主的操作（登录换 token、模型探测、执行）全部报
	// "plugin is shutting down"。
	pluginApp.StopStreams()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// main 是 c-shared 构建模式的硬性要求，永远不会被调用。
func main() {}

// nativeHostCallScoped 是宿主回调的真实实现。
func nativeHostCallScoped(callbackID string, method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := hostbridge.MarshalPayload(callbackID, payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("%s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("%s: allocate host callback payload", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}

	if hostAPI == nil {
		return nil, fmt.Errorf("%s: host api not initialized", method)
	}
	callCode := C.cpa_call_host(hostAPI, cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.cpa_free_host_buffer(hostAPI, response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("%s: host callback returned no response, code=%d", method, int(callCode))
	}
	result, errDecode := hostbridge.DecodeEnvelope(rawResponse)
	if errDecode != nil {
		return nil, fmt.Errorf("%s: %w", method, errDecode)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("%s: host callback returned code=%d", method, int(callCode))
	}
	return result, nil
}

func init() {
	hostbridge.Configure(nativeHostCallScoped)
}

var pluginVersion = app.DefaultVersion

var pluginApp = app.New(pluginVersion)
