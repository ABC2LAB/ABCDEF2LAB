package exploration

const MCPServerName = "explore_browser"
const MCPPackageVersion = "0.0.84"

// BrowserToolNames는 고정 제품 판의 탐색 도구다. 호출자가 목록을 바꿔 전역 정책을 넓힐 수 없게 복사한다.
func BrowserToolNames() []string {
	return []string{"browser_navigate", "browser_navigate_back", "browser_tabs", "browser_click", "browser_hover", "browser_drag", "browser_type", "browser_fill_form", "browser_select_option", "browser_press_key", "browser_handle_dialog", "browser_wait_for", "browser_snapshot", "browser_find", "browser_take_screenshot", "browser_resize", "browser_emulate_media", "browser_console_messages", "browser_network_requests", "browser_evaluate", "browser_close"}
}
func AllowedBrowserTool(name string) bool {
	for _, v := range BrowserToolNames() {
		if name == v {
			return true
		}
	}
	return false
}
