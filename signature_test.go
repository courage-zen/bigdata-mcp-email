package main

import "testing"

func TestAppendAgentSignature(t *testing.T) {
	both := map[string]string{"agent_name": "Bob", "workspace_name": "Acme"}

	cases := []struct {
		name     string
		body     string
		bodyType string
		mc       map[string]string
		want     string
	}{
		{
			name:     "text body, both names",
			body:     "hi",
			bodyType: "text",
			mc:       both,
			want:     "hi\n\n--\nMultica 智能体：Bob\n工作区：Acme",
		},
		{
			name:     "html body, both names",
			body:     "<p>hi</p>",
			bodyType: "html",
			mc:       both,
			want:     "<p>hi</p>\n<br>--<br>\nMultica 智能体：Bob\n<br>工作区：Acme\n<br>",
		},
		{
			name:     "text body, missing workspace_name",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"agent_name": "Bob"},
			want:     "hi",
		},
		{
			name:     "text body, missing agent_name",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"workspace_name": "Acme"},
			want:     "hi",
		},
		{
			name:     "empty agent_name value",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"agent_name": "", "workspace_name": "Acme"},
			want:     "hi",
		},
		{
			name:     "no multica context at all",
			body:     "hi",
			bodyType: "text",
			mc:       nil,
			want:     "hi",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendAgentSignature(tc.body, tc.bodyType, tc.mc)
			if got != tc.want {
				t.Errorf("appendAgentSignature(%q, %q, %v)\n got %q\nwant %q", tc.body, tc.bodyType, tc.mc, got, tc.want)
			}
		})
	}
}
