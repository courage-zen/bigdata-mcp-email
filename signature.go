package main

import "fmt"

// appendAgentSignature appends the two-line agent attribution block to the
// body when the multica context carries both an agent display name and a
// workspace display name. Either name missing or empty → body returned
// unchanged. html bodies get the same plain-text block with <br> breaks;
// text bodies get literal newlines.
func appendAgentSignature(body, bodyType string, mc map[string]string) string {
	if mc == nil {
		return body
	}
	agent := mc["agent_name"]
	workspace := mc["workspace_name"]
	if agent == "" || workspace == "" {
		return body
	}
	if bodyType == "html" {
		return fmt.Sprintf("%s\n<br>--<br>\nMultica 智能体：%s\n<br>工作区：%s\n<br>", body, agent, workspace)
	}
	return fmt.Sprintf("%s\n\n--\nMultica 智能体：%s\n工作区：%s", body, agent, workspace)
}
