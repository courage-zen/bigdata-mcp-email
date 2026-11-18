package main

import "fmt"

// appendAgentSignature appends the agent attribution block to the body when
// the multica context carries both an agent display name and a workspace
// display name. Either name missing or empty → body returned unchanged. When
// the caller's user name and email are also present, a 发起人 line is inserted
// between the agent and workspace lines. html bodies get the same plain-text
// block with <br> breaks; text bodies get literal newlines.
func appendAgentSignature(body, bodyType string, mc map[string]string) string {
	if mc == nil {
		return body
	}
	agent := mc["agent_name"]
	workspace := mc["workspace_name"]
	if agent == "" || workspace == "" {
		return body
	}
	userName, userEmail := mc["user_name"], mc["user_email"]
	initiator := ""
	if userName != "" && userEmail != "" {
		initiator = fmt.Sprintf("发起人：%s（%s）", userName, userEmail)
	}
	if bodyType == "html" {
		initiatorBr := ""
		if initiator != "" {
			initiatorBr = initiator + "\n<br>"
		}
		return fmt.Sprintf("%s\n<br>--<br>\n智能体：%s\n<br>%s工作区：%s\n<br>", body, agent, initiatorBr, workspace)
	}
	initiatorNl := ""
	if initiator != "" {
		initiatorNl = initiator + "\n"
	}
	return fmt.Sprintf("%s\n\n--\n智能体：%s\n%s工作区：%s", body, agent, initiatorNl, workspace)
}
