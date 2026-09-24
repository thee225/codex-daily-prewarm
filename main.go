// Package main implements a focused daily prewarm plugin for CLIProxyAPI.
package main

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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static cliproxy_host_api stored_host;
static int stored_host_ready;

static void store_host_api(const cliproxy_host_api* host) {
	if (host == NULL) {
		stored_host_ready = 0;
		return;
	}
	stored_host = *host;
	stored_host_ready = 1;
}

static void clear_host_api(void) {
	stored_host_ready = 0;
	stored_host.host_ctx = NULL;
	stored_host.call = NULL;
	stored_host.free_buffer = NULL;
}

static int host_api_available(void) {
	return stored_host_ready && stored_host.call != NULL;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (!host_api_available()) {
		return 1;
	}
	return stored_host.call(stored_host.host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host_ready && stored_host.free_buffer != NULL && ptr != NULL) {
		stored_host.free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pluginName = "codex-daily-prewarm"

var pluginVersion = "dev"
var app = newRuntime()

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

type hostCallbackError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *hostCallbackError) Error() string {
	if e == nil {
		return "host callback failed"
	}
	if e.Code != "" {
		return "host callback failed: " + e.Code
	}
	return "host callback failed"
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
	UsagePlugin   bool `json:"usage_plugin"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
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
		writeResponse(response, errorEnvelope("invalid_method", "method is required", false, 400))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error(), false, 500))
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
	app.shutdown()
	C.clear_host_api()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if err := json.Unmarshal(request, &lifecycle); err != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", err)
			}
		}
		if err := app.configure(lifecycle.ConfigYAML); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodUsageHandle:
		// Older hosts may still call this route. Business usage never schedules work.
		return okEnvelope(map[string]any{"accepted": true})
	case pluginabi.MethodPluginQuiesce:
		app.quiesceOnly()
		return okEnvelope(map[string]any{"stopped": true})
	case pluginabi.MethodPluginShutdown:
		app.shutdown()
		return okEnvelope(map[string]any{"stopped": true})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, false, 404), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Codex 每日预热",
			Version:          pluginVersion,
			Author:           "buyandhide",
			GitHubRepository: "https://zuowode.com:8850/buyandhide/cpa-plugin-codex-daily-prewarm",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "automatic_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用每日定时预热；宿主保留字段 enabled 仅控制插件是否加载。"},
				{Name: "dry_run", Type: pluginapi.ConfigFieldTypeBoolean, Description: "只读查询并记录 would_warm，不发模型请求；默认开启。"},
				{Name: "warm_allowlist", Type: pluginapi.ConfigFieldTypeArray, Description: "可选的灰度账号匿名指纹列表；空列表允许所有符合条件的账号。"},
				{Name: "bark_url", Type: pluginapi.ConfigFieldTypeString, Description: "定时巡检汇总的 Bark HTTPS 地址；23:00 至 08:00 免打扰。仅存入受限 CPA 配置，不出现在插件状态与日志。"},
				{Name: "reset_followup_mode", Type: pluginapi.ConfigFieldTypeString, Description: "重置补查模式：off、observe（只读观察，默认）或 active（符合条件后预热）。"},
				{Name: "schedule", Type: pluginapi.ConfigFieldTypeString, Description: "标准五段 cron；默认北京时间 05 至 23 点每小时巡检，整轮随机延迟 10–60 秒。"},
				{Name: "jobs", Type: pluginapi.ConfigFieldTypeArray, Description: "可选的多时间段任务列表；每项含 name、schedule，可选 model 和 prompt。与 schedule 二选一。"},
				{Name: "timezone", Type: pluginapi.ConfigFieldTypeString, Description: "IANA 时区，例如 Asia/Shanghai。"},
				{Name: "model", Type: pluginapi.ConfigFieldTypeString, Description: "预热模型，默认 gpt-6-luna。"},
				{Name: "fallback_model", Type: pluginapi.ConfigFieldTypeString, Description: "仅在明确不支持主模型时回退，默认 gpt-5.6-luna。"},
				{Name: "prompt", Type: pluginapi.ConfigFieldTypeString, Description: "发送给模型的短提示词，默认 hi。"},
				{Name: "expected_account_count", Type: pluginapi.ConfigFieldTypeInteger, Description: "可选的账号数量保护；默认 0 为动态发现。"},
				{Name: "unknown_quota_policy", Type: pluginapi.ConfigFieldTypeString, Description: "额度未知时必须跳过；固定为 skip。"},
				{Name: "account_spacing", Type: pluginapi.ConfigFieldTypeString, Description: "账号请求间隔，例如 30s。"},
				{Name: "retry_count", Type: pluginapi.ConfigFieldTypeInteger, Description: "明确失败时的有限重试次数；不重试结果不确定的请求。"},
				{Name: "state_path", Type: pluginapi.ConfigFieldTypeString, Description: "不含凭据的运行状态文件。"},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true, UsagePlugin: false},
	}
}

func callHost(method string, payload any) (json.RawMessage, error) {
	if C.host_api_available() == 0 {
		return nil, fmt.Errorf("CPA host callback API is unavailable")
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload")
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	code := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(code))
	}
	var env envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode host callback %s: %w", method, err)
	}
	if !env.OK || code != 0 {
		if env.Error != nil {
			return nil, &hostCallbackError{
				Code:       env.Error.Code,
				Message:    env.Error.Message,
				HTTPStatus: env.Error.HTTPStatus,
			}
		}
		return nil, fmt.Errorf("host callback %s failed, code=%d", method, int(code))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	raw, _ := json.Marshal(envelope{Error: &envelopeError{Code: code, Message: message, Retryable: retryable, HTTPStatus: status}})
	return raw
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
